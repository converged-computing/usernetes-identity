package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/converged-computing/usernetes-identity/internal/fuse"
	"github.com/converged-computing/usernetes-identity/internal/mapper"
	"github.com/converged-computing/usernetes-identity/internal/seccomp"
)

func main() {
	currUser := os.Getenv("USER")
	if currUser == "" {
		currUser = "usernetes"
	}
	defaultLog := filepath.Join(os.TempDir(), fmt.Sprintf("%s-identity.log", currUser))
	hostMin := flag.Uint("host-min", mapper.DefaultHostMin, "Start of host ID range")
	hostMax := flag.Uint("host-max", mapper.DefaultHostMax, "End of host ID range")
	hostNobody := flag.Uint("host-nobody", mapper.DefaultHostNobody, "Host ID for 'nobody'")
	contMax := flag.Uint("cont-max", mapper.DefaultContainerMax, "Max container UID")
	source := flag.String("source", "", "Host storage source")
	mount := flag.String("mount", "", "FUSE mount point")
	mountOptions := flag.String("o", "", "Standard FUSE mount options (from Podman)")
	logPath := flag.String("log", defaultLog, "Path to log file")

	flag.Parse()

	// Configure Logging
	f, err := os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
		defer f.Close()
	}

	log.Printf("--- Binary Started (PID: %d) ---", os.Getpid())

	// Handle Podman flags
	if *mountOptions != "" {
		opts := parseOptions(*mountOptions)
		if val, ok := opts["upperdir"]; ok && *source == "" {
			*source = val
		}
		// Podman appends the target mount point as the last argument
		if flag.NArg() > 0 && *mount == "" {
			*mount = flag.Arg(0)
		}
	}

	if *source == "" || *mount == "" {
		log.Fatal("Error: source and mount point are required")
	}

log.Printf("Initializing Identity Mapper...")
    m := mapper.New(mapper.Config{/*...*/})

    // 1. Create the FUSE Server instance (don't mount yet)
    // This part varies by library, but generally:
    srv, err := fuse.NewServer(*source, *mount, m)
    if err != nil {
        log.Fatalf("Failed to create FUSE server: %v", err)
    }

    // 2. Setup Signal Handling
    stop := make(chan os.Signal, 1)
    signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

    // 3. Start Mounting in the background
    log.Printf("Calling srv.Mount() in background...")
    mountErr := make(chan error, 1)
    go func() {
        mountErr <- srv.Mount()
    }()

    // 4. Wait for either a signal, a mount error, or a "Ready" state
    // Check if the mount actually appears on the host
    select {
    case err := <-mountErr:
        if err != nil {
            log.Fatalf("Mount failed immediately: %v", err)
        }
    case sig := <-stop:
        log.Printf("Received signal %v before mount finished. Exiting.", sig)
        os.Exit(0)
    case <-time.After(5 * time.Second):
        // Check if the mount point is now active
        if isMounted(*mount) {
            log.Printf("Mount confirmed active at %s", *mount)
        } else {
            log.Printf("Warning: Mount not detected after 5s. Check for fusermount issues.")
        }
    }

    // 5. Block on the server's loop
    log.Printf("Entering FUSE event loop.")
    <-stop
    log.Println("Shutting down.")
    srv.Unmount()
}

func isMounted(path string) bool {
    data, err := os.ReadFile("/proc/mounts")
    if err != nil {
        return false
    }
    return strings.Contains(string(data), path)
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
