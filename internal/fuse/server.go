package fuse

import (
	"context"
	"path/filepath"
	"syscall"
	"time"

	"github.com/converged-computing/usernetes-identity/internal/mapper"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type IdentityNode struct {
	fs.LoopbackNode
	Mapper *mapper.Mapper
	Source string
}

// Lookup is called when resolving paths. If we don't intercept this,
// the kernel will cache the unmapped host UIDs.
func (n *IdentityNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	inode, status := n.LoopbackNode.Lookup(ctx, name, out)
	if status == 0 && inode != nil {
		// Use the child inode's Path method to get its location relative to the mount root.
		// We join this with n.Source to get the absolute host path for xattr lookups.
		p := filepath.Join(n.Source, inode.Path(nil))

		// Identity Virtualization: Replace host IDs with original identities from xattrs
		out.Attr.Uid = n.Mapper.ReverseID(p, out.Attr.Uid, "user.usernetes.uid")
		out.Attr.Gid = n.Mapper.ReverseID(p, out.Attr.Gid, "user.usernetes.gid")
	}
	return inode, status
}

// Getattr ensures the container sees the identity it expects
func (n *IdentityNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	status := n.LoopbackNode.Getattr(ctx, f, out)
	if status == 0 {
		// Access the embedded Inode field to get the relative path of the current node
		p := filepath.Join(n.Source, n.Inode.Path(nil))

		// Overwrite the host-mapped IDs so the Pod sees 60000 instead of 1633 (or 65535)
		out.Uid = n.Mapper.ReverseID(p, out.Uid, "user.usernetes.uid")
		out.Gid = n.Mapper.ReverseID(p, out.Gid, "user.usernetes.gid")
	}
	return status
}

// Setattr intercepts the chown/chmod calls from the container
func (n *IdentityNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	p := filepath.Join(n.Source, n.Inode.Path(nil))

	if in.Valid&fuse.FATTR_UID != 0 {
		in.Uid = n.Mapper.ToHost(in.Uid)
	}
	if in.Valid&fuse.FATTR_GID != 0 {
		in.Gid = n.Mapper.ToHost(in.Gid)
	}

	// Let LoopbackNode apply the real syscall on the host via the translated attributes
	status := n.LoopbackNode.Setattr(ctx, f, in, out)
	if status == 0 {
		// Only persist the virtualization metadata if the host operation succeeded
		if in.Valid&fuse.FATTR_UID != 0 {
			n.Mapper.StoreID(p, "user.usernetes.uid", in.Uid)
		}
		if in.Valid&fuse.FATTR_GID != 0 {
			n.Mapper.StoreID(p, "user.usernetes.gid", in.Gid)
		}
		// Spoof the returned attributes so 'ls' shows the container IDs immediately
		out.Uid = n.Mapper.ReverseID(p, out.Uid, "user.usernetes.uid")
		out.Gid = n.Mapper.ReverseID(p, out.Gid, "user.usernetes.gid")
	}
	return status
}

func Serve(source, mount string, m *mapper.Mapper) error {
	var stat syscall.Stat_t
	if err := syscall.Stat(source, &stat); err != nil {
		return err
	}

	rootData := &fs.LoopbackRoot{
		Path: source,
		Dev:  uint64(stat.Dev),
	}

	// Ensure every file and directory in our FUSE mount uses IdentityNode
	rootData.NewNode = func(root *fs.LoopbackRoot, parent *fs.Inode, name string, st *syscall.Stat_t) fs.InodeEmbedder {
		return &IdentityNode{
			LoopbackNode: fs.LoopbackNode{
				RootData: root,
			},
			Mapper: m,
			Source: source,
		}
	}

	// Bootstrap the Root Node
	rootNode := &IdentityNode{
		LoopbackNode: fs.LoopbackNode{
			RootData: rootData,
		},
		Mapper: m,
		Source: source,
	}

	sec := time.Second
	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther: true,
			Name:       "usernetes-identity",
		},
		EntryTimeout: &sec,
		AttrTimeout:  &sec,
	}

	server, err := fs.Mount(mount, rootNode, opts)
	if err != nil {
		return err
	}

	// fs.Mount spawns the background server immediately.
	// Wait() blocks until the filesystem is unmounted.
	server.Wait()
	return nil
}
