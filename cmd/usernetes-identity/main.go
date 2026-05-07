package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/converged-computing/usernetes-identity/internal/fuse"
	"github.com/converged-computing/usernetes-identity/internal/mapper"
	"github.com/converged-computing/usernetes-identity/internal/seccomp"
)

func main() {

	// We are setting this to determine if already running
	if os.Getenv("_USERNETES_DAEMON") == "" {
		cmd := exec.Command(os.Args[0], os.Args[1:]...)
		cmd.Env = append(os.Environ(), "_USERNETES_DAEMON=1")

		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start background daemon: %v\n", err)
			os.Exit(1)
		}

		target := getMountTarget(os.Args)

		// Give the parent 10 seconds (100 * 100ms) to avoid racing
		// with the child's own 5-second fuse-overlayfs wait.
		for i := 0; i < 100; i++ {
			if isMounted(target) {
				os.Exit(0)
			}
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Fprintf(os.Stderr, "Timeout waiting for background mount at %s\n", target)
		os.Exit(1)
	}

	user := os.Getenv("USER")
	if user == "" {
		user = "unknown"
	}
	defaultLog := filepath.Join(os.TempDir(), fmt.Sprintf("usernetes-identity-%s.log", user))
	hostMin := flag.Uint("host-min", mapper.DefaultHostMin, "Start of host ID range")
	hostMax := flag.Uint("host-max", mapper.DefaultHostMax, "End of host ID range")
	hostNobody := flag.Uint("host-nobody", mapper.DefaultHostNobody, "Host ID for 'nobody'")
	contMax := flag.Uint("cont-max", mapper.DefaultContainerMax, "Max container UID")
	source := flag.String("source", "", "Host storage source")
	mount := flag.String("mount", "", "FUSE mount point")
	mountOptions := flag.String("o", "", "Standard FUSE mount options from Podman")
	logPath := flag.String("log", defaultLog, "Path to log file")

	flag.Parse()

	f, err := os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		log.SetOutput(f)
		defer f.Close()
	}

	log.Printf("--- usernetes-identity background daemon started (PID: %d) ---", os.Getpid())

	var overlayCmd *exec.Cmd
	var stagingDir string

	// Resolve Podman arguments for mount_program in storage.conf
	if *mountOptions != "" {
		log.Printf("Parsing Podman options: %s", *mountOptions)
		opts := parseOptions(*mountOptions)

		if flag.NArg() > 0 && *mount == "" {
			*mount = flag.Arg(0)
		}

		if _, hasLower := opts["lowerdir"]; hasLower {
			log.Printf("OverlayFS Union requested. Delegating to fuse-overlayfs...")
			stagingDir = *mount + "-staging"
			os.MkdirAll(stagingDir, 0755)

			// Strip out "nodev,nosuid" and other VFS poison pills that Podman sends
			// This is because in my config I had both, and it was messing it up.
			var overlayOpts []string
			for _, opt := range strings.Split(*mountOptions, ",") {
				if strings.HasPrefix(opt, "lowerdir=") || strings.HasPrefix(opt, "upperdir=") || strings.HasPrefix(opt, "workdir=") {
					overlayOpts = append(overlayOpts, opt)
				}
			}
			safeOpts := strings.Join(overlayOpts, ",")
			overlayCmd = exec.Command("fuse-overlayfs", "-f", "-o", safeOpts, stagingDir)
			overlayCmd.Stdout = f // Pipe fuse-overlayfs logs to our debug log
			overlayCmd.Stderr = f

			if err := overlayCmd.Start(); err != nil {
				log.Fatalf("Failed to start fuse-overlayfs: %v", err)
			}

			// Catch fuse-overlayfs crashes instantly instead of hanging
			errChan := make(chan error, 1)
			go func() { errChan <- overlayCmd.Wait() }()

			mounted := false

		MountLoop:
			for i := 0; i < 50; i++ {
				select {
				case err := <-errChan:
					log.Fatalf("fuse-overlayfs crashed during startup: %v", err)
				default:
					if isMounted(stagingDir) {
						mounted = true
						break MountLoop
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
			if !mounted {
				log.Printf("WARNING: fuse-overlayfs staging mount didn't register in 5s. Proceeding anyway.")
			}
			*source = stagingDir
		} else if val, ok := opts["upperdir"]; ok && *source == "" {
			*source = val
		}
	}

	if *source == "" || *mount == "" {
		log.Fatalf("Error: Source and Mount point are required.")
	}

	// Identity Mapper
	m := mapper.New(mapper.Config{
		HostMin:      uint32(*hostMin),
		HostMax:      uint32(*hostMax),
		HostNobody:   uint32(*hostNobody),
		ContainerMax: uint32(*contMax),
	})

	// Seccomp Supervisor
	if fd := os.Getenv("SECCOMP_NOTIFY_FD"); fd != "" {
		log.Printf("SECCOMP_NOTIFY_FD detected (%s). Starting Supervisor goroutine...", fd)
		go seccomp.Supervisor(fd, m)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// FUSE Mounting
	log.Printf("Attempting FUSE mount: %s -> %s", *source, *mount)
	fuseErr := make(chan error, 1)
	go func() {
		fuseErr <- fuse.Serve(*source, *mount, m)
	}()

	// gracefully handle nil on unmount - I got this as an error once
	select {
	case err := <-fuseErr:
		if err != nil {
			log.Fatalf("FUSE server crashed: %v", err)
		}
		log.Printf("FUSE server unmounted cleanly.")
	case sig := <-stop:
		log.Printf("Received signal %v. Cleaning up %s...", sig, *mount)
	}

	// And cleanup.
	syscall.Unmount(*mount, syscall.MNT_DETACH)

	if overlayCmd != nil && overlayCmd.Process != nil {
		syscall.Unmount(stagingDir, syscall.MNT_DETACH)
		overlayCmd.Process.Kill()
		os.RemoveAll(stagingDir)
	}

	log.Println("--- usernetes-identity exiting cleanly ---")
}

func parseOptions(optStr string) map[string]string {
	m := make(map[string]string)
	for _, pair := range strings.Split(optStr, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			m[kv[0]] = kv[1]
		}
	}
	return m
}

// Checking device IDs natively avoids /proc/mounts symlink and namespace bugs
func isMounted(path string) bool {
	clean := filepath.Clean(path)
	stat, err := os.Stat(clean)
	if err != nil {
		return false
	}
	parentStat, err := os.Stat(filepath.Dir(clean))
	if err != nil {
		return false
	}
	sys1, ok1 := stat.Sys().(*syscall.Stat_t)
	sys2, ok2 := parentStat.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false
	}
	// A directory is a mount point if its device ID differs from its parent.
	return sys1.Dev != sys2.Dev || sys1.Ino == sys2.Ino
}

func getMountTarget(args []string) string {
	for i, arg := range args {
		if arg == "-mount" && i+1 < len(args) {
			return args[i+1]
		}
	}
	if len(args) > 1 && !strings.HasPrefix(args[len(args)-1], "-") {
		return args[len(args)-1]
	}
	return ""
}
