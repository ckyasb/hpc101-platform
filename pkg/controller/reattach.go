package controller

import (
	"fmt"
	"log"
	"strings"
	"time"

	"hpc101-platform/lease"
)

// DiscoveryClient provides container/volume/network discovery for reattach.
// Implemented by the Podman runtime client.
type DiscoveryClient interface {
	ListContainers(labels map[string]string) ([]DiscoveryContainer, error)
	ListVolumes(labels map[string]string) ([]DiscoveryVolume, error)
}

// DiscoveryContainer represents a discovered runtime container.
type DiscoveryContainer struct {
	ID   string
	Name string
	Host string
	Port uint16
	// Labels are the platform.io/* labels on the container.
	Labels map[string]string
}

// DiscoveryVolume represents a discovered runtime volume.
type DiscoveryVolume struct {
	Name   string
	Driver string
	Labels map[string]string
}

// ReattachResult describes what was found during startup reattachment.
type ReattachResult struct {
	Reattached      int
	Orphaned        int
	OrphanVolumes   int
}

// ReattachLeases rebuilds active leases from discovered runtime containers.
// Containers with svc- prefix and platform.io/* labels become active leases.
// Stale containers (no matching label set) are logged as orphans.
// Orphan volumes (svc- prefixed, no matching container) are reported.
func ReattachLeases(store LeaseStore, client DiscoveryClient) (ReattachResult, error) {
	if client == nil {
		return ReattachResult{}, fmt.Errorf("reattach: discovery client is nil")
	}
	containers, err := client.ListContainers(map[string]string{
		"platform.io/kind": "service",
	})
	if err != nil {
		return ReattachResult{}, fmt.Errorf("reattach: list containers: %w", err)
	}

	var result ReattachResult
	activeOwners := map[string]bool{}

	for _, c := range containers {
		owner := c.Labels["platform.io/owner"]
		if owner == "" || c.Name == "" || !strings.HasPrefix(c.Name, "svc-") {
			result.Orphaned++
			log.Printf("reattach: orphan container %s (missing owner/name or not svc- prefixed)", c.ID)
			continue
		}

		activeOwners[owner] = true
		l := lease.NewLease(owner, c.ID, c.Name, c.Host, c.Port, 8*time.Hour, 30*time.Minute)
		if err := store.UpsertLease(l); err != nil {
			log.Printf("reattach: upsert lease for %s: %v", owner, err)
			continue
		}
		result.Reattached++
		log.Printf("reattach: recovered lease for %s (container %s)", owner, c.ID)
	}

	// Discover volumes: report orphan volumes with svc- prefix but no active container.
	volumes, volErr := client.ListVolumes(map[string]string{
		"platform.io/kind": "service",
	})
	if volErr != nil {
		log.Printf("reattach: list volumes: %v", volErr)
	} else {
		for _, v := range volumes {
			owner := v.Labels["platform.io/owner"]
			if owner == "" || !activeOwners[owner] {
				result.OrphanVolumes++
				log.Printf("reattach: orphan volume %s (owner=%s, driver=%s)", v.Name, owner, v.Driver)
			}
		}
	}

	return result, nil
}
