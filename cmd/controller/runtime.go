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
