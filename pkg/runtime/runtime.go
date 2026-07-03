package runtime

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"strconv"

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
	if startErr := c.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); startErr != nil {
		c.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("runtime: start %s: %w", safePrefix(resp.ID, 12), startErr)
	}
	insp, err := c.cli.ContainerInspect(ctx, resp.ID)
	if err != nil {
		c.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("runtime: inspect %s: %w", safePrefix(resp.ID, 12), err)
	}
	bindings, ok := insp.NetworkSettings.Ports["2222/tcp"]
	if !ok || len(bindings) == 0 {
		c.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("runtime: no port binding for 2222/tcp on %s", safePrefix(resp.ID, 12))
	}
	host := bindings[0].HostIP
	if host == "" || host == "0.0.0.0" {
		host = "podman-runtime.hpc101-runtime.svc.cluster.local"
	}
	p, err := strconv.ParseUint(bindings[0].HostPort, 10, 16)
	if err != nil {
		c.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("runtime: bad host port %q: %w", bindings[0].HostPort, err)
	}
	return &CreateResult{ContainerID: resp.ID, Host: host, Port: uint16(p)}, nil
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
