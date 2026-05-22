package mapper

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"strconv"
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

// ReverseID retrieves the original ID from xattrs or defaults to a safe value.
func (m *Mapper) ReverseID(path string, hostID uint32, attrName string) uint32 {
	// xattr retrieval using direct syscall to avoid CGO overhead
	pathPtr, _ := syscall.BytePtrFromString(path)
	attrPtr, _ := syscall.BytePtrFromString(attrName)

	size, _, _ := syscall.Syscall6(syscall.SYS_GETXATTR,
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(attrPtr)),
		0, 0, 0, 0)

	if int(size) > 0 {
		buf := make([]byte, size)
		syscall.Syscall6(syscall.SYS_GETXATTR, uintptr(unsafe.Pointer(pathPtr)), uintptr(unsafe.Pointer(attrPtr)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
		// Trim null terminators or whitespace to ensure ParseUint succeeds
		cleanBuf := string(bytes.Trim(buf, "\x00 "))
		val, _ := strconv.ParseUint(cleanBuf, 10, 32)
		return uint32(val)
	}

	// Fallback logic
	if hostID == 0 {
		return 0
	}
	return m.Cfg.ContainerMax
}

// StoreID persists the original container ID to the host file's xattrs.
func (m *Mapper) StoreID(path string, attrName string, id uint32) error {
	val := fmt.Sprintf("%d", id)

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
