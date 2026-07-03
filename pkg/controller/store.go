package controller

import (
	"fmt"
	"sync"

	"hpc101-platform/lease"
)

type serializedStore struct {
	mu     sync.Mutex
	leases map[string]*Lease
}

func NewSerializedStore() *serializedStore {
	return &serializedStore{leases: make(map[string]*Lease)}
}

// LookupByPrincipal returns a COPY of the lease or nil.
func (s *serializedStore) LookupByPrincipal(p string) (*Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[p]
	if !ok || l == nil {
		return nil, nil
	}
	cpy := *l
	return &cpy, nil
}

func (s *serializedStore) UpsertLease(l *Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cpy := *l
	s.leases[l.Owner] = &cpy
	return nil
}

// AllLeases returns COPIES of all stored leases (safe for read-only iteration).
func (s *serializedStore) AllLeases() []*Lease {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*Lease, 0, len(s.leases))
	for _, l := range s.leases {
		cpy := *l
		result = append(result, &cpy)
	}
	return result
}

// ReleaseLeaseIf is the ONE release operation used by HTTP release, max-life,
// and idle triggers. It holds the store lock for the full lifecycle (including
// drain and stop) to serialize release vs concurrent up/trigger.
func (s *serializedStore) ReleaseLeaseIf(principal string, trigger lease.Trigger, shouldRelease func(*Lease) bool, rt ContainerCreator, drainer BastionDrainer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	l, ok := s.leases[principal]
	if !ok || l == nil || l.State != lease.StateActive || !shouldRelease(l) {
		return fmt.Errorf("release not applicable for %s", principal)
	}
	if !l.Release(trigger) {
		return fmt.Errorf("release already in progress for %s", principal)
	}

	err := l.ExecuteRelease(func(state lease.ReleaseState) error {
		if state == lease.StateDraining && drainer != nil {
			return drainer.Drain(principal)
		}
		if state == lease.StateStopped && rt != nil {
			return rt.StopService(l.ContainerID)
		}
		return nil
	})

	if err != nil {
		l.State = lease.StateActive
		l.ReleasedBy = ""
		l.ReleasedAt = lease.Lease{}.ReleasedAt
		s.leases[principal] = l
		return err
	}
	s.leases[principal] = l
	return nil
}
