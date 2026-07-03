// Package runtime provides a Podman/Docker API client for the
// hpc101-platform controller. Wraps the Docker Go SDK to create,
// label, and manage svc- containers through the shared podman runtime.
package runtime

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
)

// Client wraps the Docker Go SDK for the controller's podman runtime access.
type Client struct {
	cli *client.Client
}

// NewClient connects to the podman runtime at the given Docker-compatible endpoint.
// endpoint is typically "tcp://podman-runtime.hpc101-runtime.svc.cluster.local:2375".
func NewClient(endpoint string) (*Client, error) {
	cli, err := client.NewClientWithOpts(client.WithHost(endpoint), client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("runtime: connect: %w", err)
	}
	return &Client{cli: cli}, nil
}

// CreateContainerRequest carries the parameters to create a student service container.
type CreateContainerRequest struct {
	// Name is the container name (svc- prefixed per plan).
	Name string
	// Image is the container image (e.g., hpc101-platform/container:latest).
	Image string
	// Labels are the platform.io/* labels applied to the container.
	Labels map[string]string
	// CPU is the CPU quota in NanoCPUs (e.g., 500_000_000 for 0.5 CPU).
	CPU int64
	// MemoryMB is the memory limit in MB.
	MemoryMB int64
	// SSHKey is the student's authorized key content.
	SSHKey string
	// MaxLife is the maximum lifetime duration.
	// MaxLife time.Duration  // TODO: connect to lease store
}

// CreateResult contains the created container's metadata.
type CreateResult struct {
	ContainerID string
}

// CreateContainer creates an svc- container with platform labels and authorized SSH key.
func (c *Client) CreateContainer(ctx context.Context, req CreateContainerRequest) (*CreateResult, error) {
	cfg := &container.Config{
		Image: req.Image,
		Labels: req.Labels,
		Env: []string{
			fmt.Sprintf("HPC101_SSH_KEY=%s", req.SSHKey),
		},
	}
	hostCfg := &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeTmpfs,
				Target: "/tmp",
				TmpfsOptions: &mount.TmpfsOptions{
					SizeBytes: 256 * 1024 * 1024, // 256MB
				},
			},
		},
		Resources: container.Resources{
			NanoCPUs: req.CPU,
			Memory:   req.MemoryMB * 1024 * 1024,
		},
	}
	resp, err := c.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, req.Name)
	if err != nil {
		return nil, fmt.Errorf("runtime: create container %s: %w", req.Name, err)
	}
	return &CreateResult{ContainerID: resp.ID}, nil
}

// StartContainer starts a previously created container.
func (c *Client) StartContainer(ctx context.Context, containerID string) error {
	if err := c.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("runtime: start %s: %w", containerID[:12], err)
	}
	return nil
}

// StopAndRemoveContainer stops and removes a container (cleanup).
func (c *Client) StopAndRemoveContainer(ctx context.Context, containerID string) error {
	if err := c.cli.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
		_ = c.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
		return fmt.Errorf("runtime: stop %s: %w", containerID[:12], err)
	}
	if err := c.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("runtime: remove %s: %w", containerID[:12], err)
	}
	return nil
}

// Close closes the Docker API client connection.
func (c *Client) Close() error {
	return c.cli.Close()
}
