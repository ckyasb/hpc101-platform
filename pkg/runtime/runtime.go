package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
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
	if req.Image == "" || req.Name == "" || req.SSHKey == "" {
		return nil, fmt.Errorf("runtime: Image, Name, and SSHKey are required")
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
			"2222/tcp": []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: "0"}},
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

// DiscoveredContainer represents a container found during discovery.
type DiscoveredContainer struct {
	ID     string
	Name   string
	Host   string
	Port   uint16
	Labels map[string]string
}

// DiscoveredVolume represents a volume found during discovery.
type DiscoveredVolume struct {
	Name   string
	Driver string
	Labels map[string]string
}

// DiscoveredNetwork represents a network found during discovery.
type DiscoveredNetwork struct {
	ID     string
	Name   string
	Driver string
	Labels map[string]string
}

// ListVolumes returns volumes matching the given labels via Docker API.
func (c *Client) ListVolumes(ctx context.Context, labels map[string]string) ([]DiscoveredVolume, error) {
	filterArgs := filters.NewArgs()
	for k, v := range labels {
		filterArgs.Add(k, v)
	}
	resp, err := c.cli.VolumeList(ctx, volume.ListOptions{Filters: filterArgs})
	if err != nil {
		return nil, fmt.Errorf("runtime: list volumes: %w", err)
	}
	result := make([]DiscoveredVolume, 0, len(resp.Volumes))
	for _, v := range resp.Volumes {
		result = append(result, DiscoveredVolume{Name: v.Name, Driver: v.Driver, Labels: v.Labels})
	}
	return result, nil
}

// ListNetworks returns networks matching the given labels via Docker API.
func (c *Client) ListNetworks(ctx context.Context, labels map[string]string) ([]DiscoveredNetwork, error) {
	filterArgs := filters.NewArgs()
	for k, v := range labels {
		filterArgs.Add(k, v)
	}
	networks, err := c.cli.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		return nil, fmt.Errorf("runtime: list networks: %w", err)
	}
	result := make([]DiscoveredNetwork, 0)
	for _, n := range networks {
		match := true
		for lk, lv := range labels {
			if n.Labels[lk] != lv {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		result = append(result, DiscoveredNetwork{ID: n.ID, Name: n.Name, Driver: n.Driver, Labels: n.Labels})
	}
	return result, nil
}

// RemoveVolume removes a volume by name.
func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	if err := c.cli.VolumeRemove(ctx, name, true); err != nil {
		return fmt.Errorf("runtime: remove volume %s: %w", name, err)
	}
	return nil
}

// RemoveNetwork removes a network by ID or name.
func (c *Client) RemoveNetwork(ctx context.Context, id string) error {
	if err := c.cli.NetworkRemove(ctx, id); err != nil {
		return fmt.Errorf("runtime: remove network %s: %w", id, err)
	}
	return nil
}

// ListContainers returns containers matching the given labels.
// Used by the controller for restart reattach and orphan discovery.
func (c *Client) ListContainers(ctx context.Context, labels map[string]string) ([]DiscoveredContainer, error) {
	filterArgs := filters.NewArgs()
	for k, v := range labels {
		filterArgs.Add(k, v)
	}
	containers, err := c.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: list containers: %w", err)
	}
	result := make([]DiscoveredContainer, 0, len(containers))
	for _, ctr := range containers {
		if len(ctr.Names) == 0 {
			continue
		}
		port := uint16(0)
		if len(ctr.Ports) > 0 {
			port = ctr.Ports[0].PublicPort
		}
		result = append(result, DiscoveredContainer{
			ID:     ctr.ID,
			Name:   strings.TrimPrefix(ctr.Names[0], "/"),
			Host:   "podman-runtime.hpc101-runtime.svc.cluster.local",
			Port:   port,
			Labels: ctr.Labels,
		})
	}
	return result, nil
}
