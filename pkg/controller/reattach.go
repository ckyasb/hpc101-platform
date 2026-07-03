package controller

import (
	"log"
	"time"

	"hpc101-platform/lease"
)

// ReattachResult describes what was found during startup reattachment.
type ReattachResult struct {
	Reattached int
	Orphaned   int
}

// DiscoveryClient provides container/volume/network discovery for reattach.
// Implemented by the Podman runtime client.
type DiscoveryClient interface {
	ListContainers(labels map[string]string) ([]DiscoveryContainer, error)
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

// ReattachLeases rebuilds active leases from discovered runtime containers.
// Containers with svc- prefix and platform.io/* labels become active leases.
// Stale containers (no matching label set) are logged as orphans.
func ReattachLeases(store LeaseStore, client DiscoveryClient) ReattachResult {
	containers, err := client.ListContainers(map[string]string{
		"platform.io/kind": "service",
	})
	if err != nil {
		log.Printf("reattach: list containers failed: %v", err)
		return ReattachResult{}
	}

	var result ReattachResult
	for _, c := range containers {
		owner := c.Labels["platform.io/owner"]
		if owner == "" || c.Name == "" {
			result.Orphaned++
			log.Printf("reattach: orphan container %s (missing owner or name)", c.ID)
			continue
		}

		l := lease.NewLease(owner, c.ID, c.Name, c.Host, c.Port, 8*time.Hour, 30*time.Minute)
		if err := store.UpsertLease(l); err != nil {
			log.Printf("reattach: upsert lease for %s: %v", owner, err)
			continue
		}
		result.Reattached++
		log.Printf("reattach: recovered lease for %s (container %s)", owner, c.ID)
	}
	return result
}
