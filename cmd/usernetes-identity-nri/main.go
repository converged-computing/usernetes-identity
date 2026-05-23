package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/containerd/nri/pkg/stub"
	"github.com/converged-computing/usernetes-identity/internal/mapper"
	"github.com/converged-computing/usernetes-identity/internal/nri"
)

func getEnvUint32(key string, fallback uint32) uint32 {
	if val, ok := os.LookupEnv(key); ok {
		if i, err := strconv.ParseUint(val, 10, 32); err == nil {
			return uint32(i)
		}
	}
	return fallback
}

func main() {
	// Flags for manual override
	flagMin := flag.Uint("host-min", uint(getEnvUint32("U7S_HOST_MIN", mapper.DefaultHostMin)), "Min host UID")
	flagMax := flag.Uint("host-max", uint(getEnvUint32("U7S_HOST_MAX", mapper.DefaultHostMax)), "Max host UID")
	flagNobody := flag.Uint("host-nobody", uint(getEnvUint32("U7S_HOST_NOBODY", mapper.DefaultHostNobody)), "Host UID for nobody")
	flag.Parse()

	m := mapper.New(mapper.Config{
		HostMin:      uint32(*flagMin),
		HostMax:      uint32(*flagMax),
		HostNobody:   uint32(*flagNobody),
		ContainerMax: mapper.DefaultContainerMax,
	})

	squeezer := &nri.Squeezer{Mapper: m}

	opts := []stub.Option{
		stub.WithPluginName("usernetes-identity-nri"),
		stub.WithPluginIdx("05"),
	}

	s, err := stub.New(squeezer, opts...)
	if err != nil {
		log.Fatalf("failed to create NRI stub: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("Usernetes NRI: Range [%d-%d] Active", m.Cfg.HostMin, m.Cfg.HostMax)
	if err := s.Run(ctx); err != nil {
		log.Fatalf("NRI plugin exited: %v", err)
	}
}
