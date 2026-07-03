package runtime

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/nat"
	"github.com/docker/docker/client"
)

type Client struct {
	cli *client.Client
}

func NewClient(endpoint string) (*Client, error) {
	cli, err := client.NewClientWithOpts(client.WithHost(endpoint), client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("runtime: connect: %w", err)
	}
	return &Client{cli: cli}, nil
}

type CreateContainerRequest struct {
	Name     string
	Image    string
	Labels   map[string]string
	CPU      int64
	MemoryMB int64
	SSHKey   string
}

type CreateResult struct {
	ContainerID string
	Host        string
	Port        uint16
}

var requiredLabels = []string{
	"platform.io/owner",
	"platform.io/kind",
	"platform.io/course",
	"platform.io/problem",
}

func (c *Client) CreateContainer(ctx context.Context, req CreateContainerRequest) (*CreateResult, error) {
	for _, k := range requiredLabels {
		if _, ok := req.Labels[k]; !ok {
			return nil, fmt.Errorf("runtime: missing required label %q on container %s", k, req.Name)
		}
	}
	if req.Image == "" {
		return nil, fmt.Errorf("runtime: Image required")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("runtime: Name required")
	}

	cfg := &container.Config{
		Image:  req.Image,
		Labels: req.Labels,
		Env:    []string{fmt.Sprintf("HPC101_SSH_KEY=%s", req.SSHKey)},
		ExposedPorts: nat.PortSet{
			"2222/tcp": struct{}{},
		},
	}
	hostCfg := &container.HostConfig{
		PortBindings: nat.PortMap{
			"2222/tcp": []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: "0"}}, // auto-allocate
		},
		Mounts: []mount.Mount{{
			Type:   mount.TypeTmpfs,
			Target: "/tmp",
			TmpfsOptions: &mount.TmpfsOptions{SizeBytes: 256 * 1024 * 1024},
		}},
		Resources: container.Resources{
			NanoCPUs: req.CPU,
			Memory:   req.MemoryMB * 1024 * 1024,
		},
	}
	resp, err := c.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, req.Name)
	if err != nil {
		return nil, fmt.Errorf("runtime: create %s: %w", req.Name, err)
	}
	if err := c.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("runtime: start %s: %w", safePrefix(resp.ID, 12), err)
	}
	insp, err := c.cli.ContainerInspect(ctx, resp.ID)
	if err != nil {
		return nil, fmt.Errorf("runtime: inspect %s: %w", safePrefix(resp.ID, 12), err)
	}
	host := insp.NetworkSettings.IPAddress
	if host == "" {
		host = "127.0.0.1"
	}
	var port uint16
	for _, bindings := range insp.NetworkSettings.Ports["2222/tcp"] {
		if bindings.HostPort != "" {
			p, _ := fmt.Sscanf(bindings.HostPort, "%d", &port) // only need 1 match
			_ = p
			break
		}
	}
	if port == 0 {
		port = 2222
	}
	return &CreateResult{ContainerID: resp.ID, Host: host, Port: port}, nil
}

func (c *Client) StartContainer(ctx context.Context, containerID string) error {
	if err := c.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("runtime: start %s: %w", safePrefix(containerID, 12), err)
	}
	return nil
}

func (c *Client) StopAndRemoveContainer(ctx context.Context, containerID string) error {
	if err := c.cli.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
		_ = c.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
		return fmt.Errorf("runtime: stop %s: %w", safePrefix(containerID, 12), err)
	}
	if err := c.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("runtime: remove %s: %w", safePrefix(containerID, 12), err)
	}
	return nil
}

func (c *Client) Close() error { return c.cli.Close() }

func safePrefix(s string, n int) string {
	if len(s) < n {
		n = len(s)
	}
	return s[:n]
}
