package fuse

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/converged-computing/usernetes-identity/internal/mapper"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type nodeRecord struct {
	relPath     string
	lookupCount uint64
}

type IdentityFileSystem struct {
	fuse.RawFileSystem
	Source string
	Mapper *mapper.Mapper

	mu     sync.Mutex
	nodes  map[uint64]*nodeRecord
	paths  map[string]uint64
	nextID uint64
}

func (fs *IdentityFileSystem) getPath(nodeID uint64) string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if n, ok := fs.nodes[nodeID]; ok { return n.relPath }
	return ""
}

func (fs *IdentityFileSystem) recordNode(relPath string) uint64 {
	if relPath == "" { relPath = "." }
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if id, ok := fs.paths[relPath]; ok {
		fs.nodes[id].lookupCount++
		return id
	}

	id := fs.nextID
	fs.nextID++
	fs.nodes[id] = &nodeRecord{relPath: relPath, lookupCount: 1}
	fs.paths[relPath] = id
	return id
}

func (fs *IdentityFileSystem) Forget(nodeID uint64, nlookup uint64) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if n, ok := fs.nodes[nodeID]; ok {
		if nlookup >= n.lookupCount {
			delete(fs.paths, n.relPath)
			delete(fs.nodes, nodeID)
		} else {
			n.lookupCount -= nlookup
		}
	}
}

func (fs *IdentityFileSystem) absPath(rel string) string {
	if rel == "." { return fs.Source }
	return filepath.Join(fs.Source, rel)
}

// --- Metadata ---

func (fs *IdentityFileSystem) GetAttr(cancel <-chan struct{}, in *fuse.GetAttrIn, out *fuse.AttrOut) fuse.Status {
	relPath := fs.getPath(in.NodeId)
	if relPath == "" { return fuse.ENOENT }

	var st syscall.Stat_t
	if err := syscall.Lstat(relPath, &st); err != nil { return fuse.ToStatus(err) }

	out.Attr.FromStat(&st)
	out.Attr.Uid = fs.Mapper.ReverseMap(fs.absPath(relPath), st.Uid)
	out.AttrValid = 10 
	return fuse.OK
}

func (fs *IdentityFileSystem) SetAttr(cancel <-chan struct{}, in *fuse.SetAttrIn, out *fuse.AttrOut) fuse.Status {
	relPath := fs.getPath(in.NodeId)
	if relPath == "" { return fuse.ENOENT }

	if in.Valid&(fuse.FATTR_UID|fuse.FATTR_GID) != 0 {
		uid, gid := -1, -1
		if in.Valid&fuse.FATTR_UID != 0 { uid = int(fs.Mapper.ToHost(in.Uid)) }
		if in.Valid&fuse.FATTR_GID != 0 { gid = int(fs.Mapper.ToHost(in.Gid)) }
		_ = os.Lchown(relPath, uid, gid)
	}

	if in.Valid&fuse.FATTR_MODE != 0 { _ = syscall.Chmod(relPath, in.Mode) }
	if in.Valid&(fuse.FATTR_ATIME|fuse.FATTR_MTIME) != 0 {
		_ = os.Chtimes(relPath, time.Unix(int64(in.Atime), int64(in.Atimensec)), time.Unix(int64(in.Mtime), int64(in.Mtimensec)))
	}

	return fs.GetAttr(cancel, &fuse.GetAttrIn{InHeader: in.InHeader}, out)
}

// --- Traversal ---

func (fs *IdentityFileSystem) Lookup(cancel <-chan struct{}, header *fuse.InHeader, name string, out *fuse.EntryOut) fuse.Status {
	parentRel := fs.getPath(header.NodeId)
	if parentRel == "" { return fuse.ENOENT }

	targetRel := filepath.Join(parentRel, name)
	if parentRel == "." { targetRel = name }

	// SURGICAL LOG: Tripwire for deep recursion
	if len(targetRel) > 200 {
		log.Printf("⚠️ DEEP LOOKUP TRIPWIRE: %s", targetRel)
	}
	// SURGICAL LOG: Tracking OverlayFS graph structural roots
	if name == "merged" || name == "diff" || name == "work" {
		log.Printf("📂 OVERLAY STRUCTURE LOOKUP: %s", targetRel)
	}

	var st syscall.Stat_t
	if err := syscall.Lstat(targetRel, &st); err != nil { return fuse.ToStatus(err) }

	out.Attr.FromStat(&st)
	out.NodeId = fs.recordNode(targetRel)
	out.Attr.Uid = fs.Mapper.ReverseMap(fs.absPath(targetRel), st.Uid)

	out.EntryValid = 10 
	out.AttrValid = 10 
	return fuse.OK
}

func (fs *IdentityFileSystem) ReadDirPlus(cancel <-chan struct{}, in *fuse.ReadIn, out *fuse.DirEntryList) fuse.Status {
	relPath := fs.getPath(in.NodeId)
	if relPath == "" { return fuse.ENOENT }

	files, err := os.ReadDir(relPath)
	if err != nil { return fuse.ToStatus(err) }

	for i := int(in.Offset); i < len(files); i++ {
		f := files[i]
		targetRel := filepath.Join(relPath, f.Name())
		if relPath == "." { targetRel = f.Name() }
		
		var st syscall.Stat_t
		if err := syscall.Lstat(targetRel, &st); err != nil { continue }

		id := fs.recordNode(targetRel)
		d := fuse.DirEntry{ Name: f.Name(), Mode: uint32(st.Mode), Ino: st.Ino }

		entry := out.AddDirLookupEntry(d)
		if entry == nil { break }

		entry.Attr.FromStat(&st)
		entry.NodeId = id
		entry.Attr.Uid = fs.Mapper.ReverseMap(fs.absPath(targetRel), st.Uid)
	}
	return fuse.OK
}

// --- Mutations ---

func (fs *IdentityFileSystem) Mkdir(cancel <-chan struct{}, in *fuse.MkdirIn, name string, out *fuse.EntryOut) fuse.Status {
	parentRel := fs.getPath(in.NodeId)
	if parentRel == "" { return fuse.ENOENT }
	targetRel := filepath.Join(parentRel, name)
	if parentRel == "." { targetRel = name }

	if err := os.Mkdir(targetRel, os.FileMode(in.Mode)); err != nil { return fuse.ToStatus(err) }
	_ = os.Lchown(targetRel, int(fs.Mapper.ToHost(in.Uid)), int(fs.Mapper.ToHost(in.Gid)))

	var st syscall.Stat_t
	_ = syscall.Lstat(targetRel, &st)
	out.Attr.FromStat(&st)
	out.NodeId = fs.recordNode(targetRel)
	out.Attr.Uid = in.Uid
	return fuse.OK
}

func (fs *IdentityFileSystem) Create(cancel <-chan struct{}, in *fuse.CreateIn, name string, out *fuse.CreateOut) fuse.Status {
	parentRel := fs.getPath(in.NodeId)
	if parentRel == "" { return fuse.ENOENT }
	targetRel := filepath.Join(parentRel, name)
	if parentRel == "." { targetRel = name }

	fd, err := syscall.Open(targetRel, int(in.Flags)|syscall.O_CREAT, in.Mode)
	if err != nil { return fuse.ToStatus(err) }

	_ = os.Lchown(targetRel, int(fs.Mapper.ToHost(in.Uid)), int(fs.Mapper.ToHost(in.Gid)))
	var st syscall.Stat_t
	_ = syscall.Fstat(fd, &st)

	out.Attr.FromStat(&st)
	out.NodeId = fs.recordNode(targetRel)
	out.Attr.Uid = in.Uid
	out.Fh = uint64(fd)
	return fuse.OK
}

func (fs *IdentityFileSystem) Symlink(cancel <-chan struct{}, header *fuse.InHeader, target string, name string, out *fuse.EntryOut) fuse.Status {
	parentRel := fs.getPath(header.NodeId)
	if parentRel == "" { return fuse.ENOENT }
	targetRel := filepath.Join(parentRel, name)
	if parentRel == "." { targetRel = name }

	if err := syscall.Symlink(target, targetRel); err != nil { return fuse.ToStatus(err) }
	_ = os.Lchown(targetRel, int(fs.Mapper.ToHost(header.Uid)), int(fs.Mapper.ToHost(header.Gid)))

	var st syscall.Stat_t
	_ = syscall.Lstat(targetRel, &st)
	out.Attr.FromStat(&st)
	out.NodeId = fs.recordNode(targetRel)
	return fuse.OK
}

func (fs *IdentityFileSystem) Mknod(cancel <-chan struct{}, in *fuse.MknodIn, name string, out *fuse.EntryOut) fuse.Status {
	parentRel := fs.getPath(in.NodeId)
	if parentRel == "" { return fuse.ENOENT }
	targetRel := filepath.Join(parentRel, name)
	if parentRel == "." { targetRel = name }

	if err := syscall.Mknod(targetRel, in.Mode, int(in.Rdev)); err != nil { return fuse.ToStatus(err) }

	var st syscall.Stat_t
	_ = syscall.Lstat(targetRel, &st)
	out.Attr.FromStat(&st)
	out.NodeId = fs.recordNode(targetRel)
	return fuse.OK
}

func (fs *IdentityFileSystem) Link(cancel <-chan struct{}, in *fuse.LinkIn, name string, out *fuse.EntryOut) fuse.Status {
	oldRel := fs.getPath(in.Oldnodeid)
	if oldRel == "" { return fuse.ENOENT }
	parentRel := fs.getPath(in.InHeader.NodeId)
	if parentRel == "" { return fuse.ENOENT }

	targetRel := filepath.Join(parentRel, name)
	if parentRel == "." { targetRel = name }

	if err := syscall.Link(oldRel, targetRel); err != nil { return fuse.ToStatus(err) }

	var st syscall.Stat_t
	_ = syscall.Lstat(targetRel, &st)
	out.Attr.FromStat(&st)
	out.NodeId = fs.recordNode(targetRel)
	out.Attr.Uid = fs.Mapper.ReverseMap(fs.absPath(targetRel), st.Uid)
	return fuse.OK
}

func (fs *IdentityFileSystem) Readlink(cancel <-chan struct{}, header *fuse.InHeader) ([]byte, fuse.Status) {
	relPath := fs.getPath(header.NodeId)
	target, err := os.Readlink(relPath)
	if err != nil { return nil, fuse.ToStatus(err) }
	return []byte(target), fuse.OK
}

// --- Extended Attributes (Critical for OverlayFS) ---

func (fs *IdentityFileSystem) GetXAttr(cancel <-chan struct{}, header *fuse.InHeader, attr string, dest []byte) (uint32, fuse.Status) {
	relPath := fs.getPath(header.NodeId)
	if relPath == "" { return 0, fuse.ENOENT }

	sz, err := syscall.Getxattr(relPath, attr, dest)
	
	// SURGICAL LOG: Track specifically how the tmpfs handles trusted.overlay requests
	if strings.HasPrefix(attr, "trusted.overlay") {
		errStr := "nil"
		if err != nil { errStr = err.Error() }
		log.Printf("🔍 GETXATTR Overlay Marker: path=%s attr=%s result_size=%d err=%s", relPath, attr, sz, errStr)
	}

	if err != nil {
		if err == syscall.ENODATA { return 0, fuse.ToStatus(syscall.ENODATA) }
		// If tmpfs throws ENOTSUP (Operation Not Supported), OverlayFS will silently fail and loop.
		return 0, fuse.ToStatus(err)
	}
	return uint32(sz), fuse.OK
}

func (fs *IdentityFileSystem) SetXAttr(cancel <-chan struct{}, in *fuse.SetXAttrIn, attr string, data []byte) fuse.Status {
	relPath := fs.getPath(in.NodeId)
	if relPath == "" { return fuse.ENOENT }
	
	err := syscall.Setxattr(relPath, attr, data, int(in.Flags))
	
	// SURGICAL LOG: Track OverlayFS write attempts
	if strings.HasPrefix(attr, "trusted.overlay") {
		errStr := "nil"
		if err != nil { errStr = err.Error() }
		log.Printf("✍️ SETXATTR Overlay Marker: path=%s attr=%s data=%s err=%s", relPath, attr, string(data), errStr)
	}

	return fuse.ToStatus(err)
}

func (fs *IdentityFileSystem) ListXAttr(cancel <-chan struct{}, header *fuse.InHeader, dest []byte) (uint32, fuse.Status) {
	relPath := fs.getPath(header.NodeId)
	if relPath == "" { return 0, fuse.ENOENT }
	
	sz, err := syscall.Listxattr(relPath, dest)
	if err != nil { return 0, fuse.ToStatus(err) }
	return uint32(sz), fuse.OK
}

func (fs *IdentityFileSystem) RemoveXAttr(cancel <-chan struct{}, header *fuse.InHeader, attr string) fuse.Status {
	relPath := fs.getPath(header.NodeId)
	if relPath == "" { return fuse.ENOENT }
	
	err := syscall.Removexattr(relPath, attr)
	return fuse.ToStatus(err)
}

// --- I/O & Plumbing ---

func (fs *IdentityFileSystem) Open(cancel <-chan struct{}, in *fuse.OpenIn, out *fuse.OpenOut) fuse.Status {
	relPath := fs.getPath(in.NodeId)
	fd, err := syscall.Open(relPath, int(in.Flags), 0)
	if err != nil { return fuse.ToStatus(err) }
	out.Fh = uint64(fd)
	return fuse.OK
}

func (fs *IdentityFileSystem) Read(cancel <-chan struct{}, in *fuse.ReadIn, buf []byte) (fuse.ReadResult, fuse.Status) {
	n, err := syscall.Pread(int(in.Fh), buf, int64(in.Offset))
	if err != nil { return nil, fuse.ToStatus(err) }
	return fuse.ReadResultData(buf[:n]), fuse.OK
}

func (fs *IdentityFileSystem) Write(cancel <-chan struct{}, in *fuse.WriteIn, data []byte) (uint32, fuse.Status) {
	n, err := syscall.Pwrite(int(in.Fh), data, int64(in.Offset))
	return uint32(n), fuse.ToStatus(err)
}

func (fs *IdentityFileSystem) Rename(cancel <-chan struct{}, in *fuse.RenameIn, old, new string) fuse.Status {
	oldRel := filepath.Join(fs.getPath(in.NodeId), old)
	newRel := filepath.Join(fs.getPath(in.Newdir), new)
	if fs.getPath(in.NodeId) == "." { oldRel = old }
	if fs.getPath(in.Newdir) == "." { newRel = new }
	
	return fuse.ToStatus(syscall.Rename(oldRel, newRel))
}

func (fs *IdentityFileSystem) Unlink(cancel <-chan struct{}, header *fuse.InHeader, name string) fuse.Status {
	rel := filepath.Join(fs.getPath(header.NodeId), name)
	if fs.getPath(header.NodeId) == "." { rel = name }
	return fuse.ToStatus(syscall.Unlink(rel))
}

func (fs *IdentityFileSystem) Rmdir(cancel <-chan struct{}, header *fuse.InHeader, name string) fuse.Status {
	rel := filepath.Join(fs.getPath(header.NodeId), name)
	if fs.getPath(header.NodeId) == "." { rel = name }
	return fuse.ToStatus(syscall.Rmdir(rel))
}

func (fs *IdentityFileSystem) Release(cancel <-chan struct{}, in *fuse.ReleaseIn) { _ = syscall.Close(int(in.Fh)) }
func (fs *IdentityFileSystem) Access(cancel <-chan struct{}, in *fuse.AccessIn) fuse.Status { return fuse.OK }

func (fs *IdentityFileSystem) StatFs(cancel <-chan struct{}, in *fuse.InHeader, out *fuse.StatfsOut) fuse.Status {
	var st syscall.Statfs_t
	if err := syscall.Statfs(".", &st); err != nil { return fuse.ToStatus(err) }
	
	out.Blocks, out.Bfree, out.Bavail = st.Blocks, st.Bfree, st.Bavail
	out.Files, out.Ffree = st.Files, st.Ffree
	out.Bsize, out.Frsize, out.NameLen = uint32(st.Bsize), uint32(st.Frsize), uint32(st.Namelen)
	return fuse.OK
}

func Serve(source, mount string, m *mapper.Mapper) error {
	realSource, _ := filepath.EvalSymlinks(source)
	realSource, _ = filepath.Abs(realSource)

	if err := os.Chdir(realSource); err != nil {
		return fmt.Errorf("failed to chdir into physical source: %v", err)
	}

	fs := &IdentityFileSystem{
		RawFileSystem: fuse.NewDefaultRawFileSystem(),
		Source:        realSource,
		Mapper:        m,
		nodes:         make(map[uint64]*nodeRecord),
		paths:         make(map[string]uint64),
		nextID:        2,
	}

	fs.nodes[1] = &nodeRecord{relPath: ".", lookupCount: 1}
	fs.paths["."] = 1

	log.Printf("Relative CWD anchored to %s", realSource)
	server, err := fuse.NewServer(fs, mount, &fuse.MountOptions{
		AllowOther: false,
		Name:       "usernetes-identity",
	})
	if err != nil { return err }
	
	server.Serve()
	return nil
}
