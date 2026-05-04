//go:build linux
// +build linux

package seccomp

import (
	"log"
	"strconv"
	"syscall"

	"github.com/converged-computing/usernetes-identity/internal/mapper"
	libseccomp "github.com/seccomp/libseccomp-golang"
)

// Supervisor listens for Seccomp notifications and spoofs the return values.
func Supervisor(fdStr string, m *mapper.Mapper) {
	fdInt, err := strconv.Atoi(fdStr)
	if err != nil {
		log.Fatalf("Invalid Seccomp FD: %v", err)
	}

	// Cast the standard FD to the libseccomp specific type
	fd := libseccomp.ScmpFd(fdInt)

	for {
		// 1. Receive the notification from the kernel
		req, err := libseccomp.NotifReceive(fd)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			log.Printf("Seccomp receive error: %v", err)
			break
		}

		// 2. Verify the process hasn't died (Hardened against PID reuse attacks)
		if err := libseccomp.NotifIDValid(fd, req.ID); err != nil {
			continue
		}

		// 3. Spoof the response: return the ContainerMax UID
		// In a fully dynamic setup, you would map this based on the specific PID context.
		resp := &libseccomp.ScmpNotifResp{
			ID:    req.ID,
			Val:   uint64(m.Cfg.ContainerMax),
			Error: 0,
			Flags: 0,
		}

		// 4. Send the spoofed identity back to the kernel
		if err := libseccomp.NotifRespond(fd, resp); err != nil {
			log.Printf("Seccomp send error: %v", err)
		}
	}
}
