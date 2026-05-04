package mapper

import (
	"fmt"
	"hash/fnv"
	"syscall"
	"unsafe"
)

// Defaults based on userns-uid-map=0:0:1 --userns-uid-map=1:1:1999 --userns-uid-map=65534:2000:2
const (
	DefaultHostMin      = 1
	DefaultHostMax      = 1999
	DefaultHostNobody   = 2000
	DefaultContainerMax = 65535
)

type Config struct {
	HostMin, HostMax, HostNobody, ContainerMax uint32
}

// Mapper handles the bidirectional translation between container and host IDs.
type Mapper struct {
	Cfg Config
}

func New(c Config) *Mapper {
	return &Mapper{Cfg: c}
}

// ToHost maps high container UIDs into the host's 2K window.
func (m *Mapper) ToHost(cUID uint32) uint32 {
	if cUID == 0 {
		return 0
	}
	if cUID >= 65534 {
		return m.Cfg.HostNobody
	}

	size := m.Cfg.HostMax - m.Cfg.HostMin + 1
	h := fnv.New32a()
	h.Write([]byte(fmt.Sprint(cUID)))
	return m.Cfg.HostMin + (h.Sum32() % size)
}

// ReverseMap retrieves the original UID from xattrs or defaults to a safe value.
func (m *Mapper) ReverseMap(path string, hostUID uint32) uint32 {
	buf := make([]byte, 16)
	attrName := "user.usernetes.uid"

	// xattr retrieval using direct syscall to avoid CGO overhead
	pathPtr, _ := syscall.BytePtrFromString(path)
	attrPtr, _ := syscall.BytePtrFromString(attrName)

	size, _, _ := syscall.Syscall6(syscall.SYS_GETXATTR,
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(attrPtr)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)), 0, 0)

	if int(size) > 0 {
		var orig uint32
		if _, err := fmt.Sscanf(string(buf[:size]), "%d", &orig); err == nil {
			return orig
		}
	}

	// Fallback logic
	if hostUID == 0 {
		return 0
	}
	return m.Cfg.ContainerMax
}

// StoreUID persists the original container UID to the host file's xattrs.
func (m *Mapper) StoreUID(path string, cUID uint32) error {
	attrName := "user.usernetes.uid"
	val := fmt.Sprintf("%d", cUID)

	pathPtr, _ := syscall.BytePtrFromString(path)
	attrPtr, _ := syscall.BytePtrFromString(attrName)
	valPtr, _ := syscall.BytePtrFromString(val)

	_, _, errno := syscall.Syscall6(syscall.SYS_SETXATTR,
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(attrPtr)),
		uintptr(unsafe.Pointer(valPtr)),
		uintptr(len(val)), 0, 0)

	if errno != 0 {
		return errno
	}
	return nil
}
