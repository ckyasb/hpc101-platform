//go:build !go1.25

package main

import (
	"fmt"

	"hpc101-platform/controller"
)

// Stub for go < 1.25: runtime requires go >= 1.25 (Docker SDK transitive dep).
// The production Docker image uses golang:1.25-alpine.
func newRuntimeAdapter(endpoint string) (controller.ContainerCreator, error) {
	return nil, fmt.Errorf("runtime requires go >= 1.25 (build with golang:1.25-alpine)")
}
