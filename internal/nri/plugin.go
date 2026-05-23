package nri

import (
	"context"
	"fmt"
	"log"

	"github.com/containerd/nri/pkg/api"
	"github.com/converged-computing/usernetes-identity/internal/mapper"
)

type Squeezer struct {
	Mapper *mapper.Mapper
}

func (s *Squeezer) CreateContainer(ctx context.Context, pod *api.PodSandbox, container *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	// 1. Discover the requested UID from the provided protobuf structure (line 350/423)
	requestedUID := uint32(0)
	if container.User != nil {
		requestedUID = container.User.Uid
	}

	// 2. Use the common mapper to calculate the squashed host ID
	// This ensures synchronization between NRI, FUSE, and Seccomp
	hostUID := s.Mapper.ToHost(requestedUID)

	log.Printf("NRI: Container %s (UID %d) -> Squashed Host ID %d", container.Name, requestedUID, hostUID)

	// 3. Since this NRI version lacks UidMappings, we pass the decision via Annotations.
	// usernetes-identity (the mount program) can read these from config.json.
	return &api.ContainerAdjustment{
		Annotations: map[string]string{
			"user.usernetes.squash.uid":      fmt.Sprintf("%d", requestedUID),
			"user.usernetes.squash.host_id":  fmt.Sprintf("%d", hostUID),
			"user.usernetes.squash.host_min": fmt.Sprintf("%d", s.Mapper.Cfg.HostMin),
			"user.usernetes.squash.host_max": fmt.Sprintf("%d", s.Mapper.Cfg.HostMax),
		},
	}, nil, nil
}
