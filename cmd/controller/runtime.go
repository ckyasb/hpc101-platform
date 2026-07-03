//go:build go1.25
// +build go1.25

package main

import (
	"context"
	"fmt"

	"hpc101-platform/controller"
	"hpc101-platform/runtime"
)

// runtimeAdapter implements controller.ContainerCreator using pkg/runtime.
type runtimeAdapter struct {
	client *runtime.Client
}

func newRuntimeAdapter(endpoint string) (*runtimeAdapter, error) {
	cli, err := runtime.NewClient(endpoint)
	if err != nil {
		return nil, fmt.Errorf("controller: runtime adapter: %w", err)
	}
	return &runtimeAdapter{client: cli}, nil
}

func (a *runtimeAdapter) CreateService(req controller.CreateServiceRequest) (*controller.ServiceResult, error) {
	name := "svc-" + req.Principal
	labels := map[string]string{
		"platform.io/owner":   req.Principal,
		"platform.io/kind":    "service",
		"platform.io/course":  req.Course,
		"platform.io/problem": req.Problem,
	}
	cpu := req.CPULimit
	if cpu <= 0 {
		cpu = 500_000_000
	}
	mem := req.MemoryMB
	if mem <= 0 {
		mem = 256
	}
	res, err := a.client.CreateContainer(context.Background(), runtime.CreateContainerRequest{
		Name:     name,
		Image:    req.Image,
		Labels:   labels,
		CPU:      cpu,
		MemoryMB: mem,
		SSHKey:   req.SSHKey,
	})
	if err != nil {
		return nil, fmt.Errorf("controller: create service: %w", err)
	}
	return &controller.ServiceResult{
		ContainerID: res.ContainerID,
		Host:        res.Host,
		Port:        res.Port,
	}, nil
}

func (a *runtimeAdapter) StopService(containerID string) error {
	return a.client.StopAndRemoveContainer(context.Background(), containerID)
}

func (a *runtimeAdapter) ListContainers(labels map[string]string) ([]controller.DiscoveryContainer, error) {
	ctrs, err := a.client.ListContainers(context.Background(), labels)
	if err != nil {
		return nil, err
	}
	result := make([]controller.DiscoveryContainer, len(ctrs))
	for i, c := range ctrs {
		result[i] = controller.DiscoveryContainer{
			ID: c.ID, Name: c.Name, Host: c.Host, Port: c.Port,
			Labels: c.Labels,
		}
	}
	return result, nil
}

func (a *runtimeAdapter) ListVolumes(labels map[string]string) ([]controller.DiscoveryVolume, error) {
	vols, err := a.client.ListVolumes(context.Background(), labels)
	if err != nil {
		return nil, err
	}
	result := make([]controller.DiscoveryVolume, len(vols))
	for i, v := range vols {
		result[i] = controller.DiscoveryVolume{Name: v.Name, Driver: v.Driver, Labels: v.Labels}
	}
	return result, nil
}

func (a *runtimeAdapter) ListNetworks(labels map[string]string) ([]controller.DiscoveryNetwork, error) {
	nets, err := a.client.ListNetworks(context.Background(), labels)
	if err != nil {
		return nil, err
	}
	result := make([]controller.DiscoveryNetwork, len(nets))
	for i, n := range nets {
		result[i] = controller.DiscoveryNetwork{ID: n.ID, Name: n.Name, Driver: n.Driver, Labels: n.Labels}
	}
	return result, nil
}

func (a *runtimeAdapter) RemoveVolume(ctx context.Context, name string) error {
	return a.client.RemoveVolume(ctx, name)
}

func (a *runtimeAdapter) RemoveNetwork(ctx context.Context, id string) error {
	return a.client.RemoveNetwork(ctx, id)
}
