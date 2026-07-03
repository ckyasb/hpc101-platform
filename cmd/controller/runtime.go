//go:build go1.25

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

func (a *runtimeAdapter) CreateService(principal, image, sshKey, course, problem string) (*controller.ServiceResult, error) {
	name := "svc-" + principal
	labels := map[string]string{
		"platform.io/owner":   principal,
		"platform.io/kind":    "service",
		"platform.io/course":  course,
		"platform.io/problem": problem,
	}
	res, err := a.client.CreateContainer(context.Background(), runtime.CreateContainerRequest{
		Name:     name,
		Image:    image,
		Labels:   labels,
		CPU:      500_000_000,
		MemoryMB: 256,
		SSHKey:   sshKey,
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
