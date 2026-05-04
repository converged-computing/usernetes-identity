package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/converged-computing/usernetes-identity/internal/fuse"
	"github.com/converged-computing/usernetes-identity/internal/mapper"
	"github.com/converged-computing/usernetes-identity/internal/seccomp"
)

func main() {
	hostMin := flag.Uint("host-min", mapper.DefaultHostMin, "Start of host ID range")
	hostMax := flag.Uint("host-max", mapper.DefaultHostMax, "End of host ID range")
	hostNobody := flag.Uint("host-nobody", mapper.DefaultHostNobody, "Host ID for 'nobody'")
	contMax := flag.Uint("cont-max", mapper.DefaultContainerMax, "Max container UID")
	source := flag.String("source", "", "Host storage source")
	mount := flag.String("mount", "", "FUSE mount point")
	flag.Parse()

	m := mapper.New(mapper.Config{
		HostMin: uint32(*hostMin), HostMax: uint32(*hostMax),
		HostNobody: uint32(*hostNobody), ContainerMax: uint32(*contMax),
	})

	// Handle Seccomp notifications if the FD is passed
	if fd := os.Getenv("SECCOMP_NOTIFY_FD"); fd != "" {
		go seccomp.Supervisor(fd, m)
	}

	// Unmount on SIGTERM/SIGINT
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		log.Println("Unmounting...")
		syscall.Unmount(*mount, 0)
		os.Exit(0)
	}()

	// Serve the FUSE filesystem
	log.Fatal(fuse.Serve(*source, *mount, m))
}
