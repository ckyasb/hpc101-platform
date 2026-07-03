package controller

import (
	"context"
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
	ListNetworks(labels map[string]string) ([]DiscoveryNetwork, error)
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

// DiscoveryNetwork represents a discovered runtime network.
type DiscoveryNetwork struct {
	ID     string
	Name   string
	Driver string
	Labels map[string]string
}

// ResourceCleaner is an optional interface for reclaiming orphan resources.
type ResourceCleaner interface {
	RemoveVolume(ctx context.Context, name string) error
	RemoveNetwork(ctx context.Context, id string) error
}

// ReattachResult describes what was found during startup reattachment.
type ReattachResult struct {
	Reattached       int
	Orphaned         int
	OrphanVolumes    int
	OrphanNetworks   int
	ReclaimedVolumes int
	ReclaimedNets    int
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
		course := c.Labels["platform.io/course"]
		problem := c.Labels["platform.io/problem"]
		if owner == "" || course == "" || problem == "" || c.Name == "" || !strings.HasPrefix(c.Name, "svc-") {
			result.Orphaned++
			log.Printf("reattach: orphan container %s (incomplete labels: owner=%s course=%s problem=%s)", c.ID, owner, course, problem)
			continue
		}
		activeOwners[owner+"/"+course+"/"+problem] = true
		l := lease.NewLease(owner, c.ID, c.Name, c.Host, c.Port, 8*time.Hour, 30*time.Minute)
		if err := store.UpsertLease(l); err != nil {
			log.Printf("reattach: upsert lease for %s: %v", owner, err)
			continue
		}
		result.Reattached++
		log.Printf("reattach: recovered lease for %s (container %s)", owner, c.ID)
	}

	// Discover volumes: reclaim svc- orphan volumes; csj- never touched.
	volumes, volErr := client.ListVolumes(map[string]string{
		"platform.io/kind": "service",
	})
	if volErr != nil {
		log.Printf("reattach: list volumes: %v", volErr)
	} else {
		cleaner, hasCleaner := interface{}(client).(ResourceCleaner)
		for _, v := range volumes {
			if !strings.HasPrefix(v.Name, "svc-") {
				continue
			}
			owner := v.Labels["platform.io/owner"]
			course := v.Labels["platform.io/course"]
			problem := v.Labels["platform.io/problem"]
			svcID := owner + "/" + course + "/" + problem
			if owner == "" || course == "" || problem == "" || !activeOwners[svcID] {
				result.OrphanVolumes++
				if hasCleaner {
					if err := cleaner.RemoveVolume(context.Background(), v.Name); err != nil {
						log.Printf("reattach: reclaim volume %s: %v", v.Name, err)
					} else {
						result.ReclaimedVolumes++
						log.Printf("reattach: reclaimed orphan volume %s", v.Name)
					}
				}
			}
		}
	}

	// Discover networks: reclaim svc- orphan networks; csj- never touched.
	networks, netErr := client.ListNetworks(map[string]string{
		"platform.io/kind": "service",
	})
	if netErr != nil {
		log.Printf("reattach: list networks: %v", netErr)
	} else {
		cleaner, hasCleaner := interface{}(client).(ResourceCleaner)
		for _, n := range networks {
			if !strings.HasPrefix(n.Name, "svc-") {
				continue
			}
			owner := n.Labels["platform.io/owner"]
			course := n.Labels["platform.io/course"]
			problem := n.Labels["platform.io/problem"]
			svcID := owner + "/" + course + "/" + problem
			if owner == "" || course == "" || problem == "" || !activeOwners[svcID] {
				result.OrphanNetworks++
				if hasCleaner {
					if err := cleaner.RemoveNetwork(context.Background(), n.ID); err != nil {
						log.Printf("reattach: reclaim network %s: %v", n.Name, err)
					} else {
						result.ReclaimedNets++
						log.Printf("reattach: reclaimed orphan network %s", n.Name)
					}
				}
			}
		}
	}

	return result, nil
}
