package fuse

import (
	"syscall"

	"github.com/converged-computing/usernetes-identity/internal/mapper"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type IdentityFileSystem struct {
	fuse.RawFileSystem
	Source string
	Mapper *mapper.Mapper
}

// SetAttr intercepts the chown/chmod calls from the container.
func (fs *IdentityFileSystem) SetAttr(cancel <-chan struct{}, in *fuse.SetAttrIn, out *fuse.AttrOut) fuse.Status {
	if in.Valid&fuse.FATTR_UID != 0 {
		// Map the container UID to our 2K host range
		hostUID := fs.Mapper.ToHost(in.Uid)

		// Hardened: Perform the real syscall on the host
		if err := syscall.Chown(fs.Source, int(hostUID), -1); err != nil {
			return fuse.ToStatus(err)
		}
	}
	return fuse.OK
}

// GetAttr ensures the container sees the identity it expects (the "Lie")
func (fs *IdentityFileSystem) GetAttr(cancel <-chan struct{}, in *fuse.GetAttrIn, out *fuse.AttrOut) fuse.Status {
	var st syscall.Stat_t
	if err := syscall.Lstat(fs.Source, &st); err != nil {
		return fuse.ToStatus(err)
	}

	out.FromStat(&st)

	// Spoof the UID: Return the original container UID (e.g., 20000)
	// instead of the host UID (e.g., 105)
	out.Uid = fs.Mapper.ReverseMap(fs.Source, st.Uid)
	return fuse.OK
}

func Serve(source, mount string, m *mapper.Mapper) error {
	fs := &IdentityFileSystem{
		RawFileSystem: fuse.NewDefaultRawFileSystem(),
		Source:        source,
		Mapper:        m,
	}

	server, err := fuse.NewServer(fs, mount, &fuse.MountOptions{
		AllowOther: true,
		Name:       "usernetes-identity",
	})
	if err != nil {
		return err
	}

	server.Serve()
	return nil
}
