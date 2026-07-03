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

func (s *serializedStore) LookupByPrincipal(p string) (*Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[p]
	if !ok {
		return nil, nil
	}
	return l, nil
}

func (s *serializedStore) UpsertLease(l *Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leases[l.Owner] = l
	return nil
}

func (s *serializedStore) ReleaseLease(principal string, rt ContainerCreator) error {
	s.mu.Lock()
	l, ok := s.leases[principal]
	if !ok || l == nil {
		s.mu.Unlock()
		return fmt.Errorf("no active lease for %s", principal)
	}
	if !l.Release(lease.TriggerManual) {
		s.mu.Unlock()
		return fmt.Errorf("release failed for %s", principal)
	}
	s.mu.Unlock()

	if err := l.ExecuteRelease(func(state lease.ReleaseState) error {
		if state == lease.StateStopped && rt != nil {
			return rt.StopService(l.ContainerID)
		}
		return nil
	}); err != nil {
		l.State = lease.StateActive
		s.UpsertLease(l)
		return err
	}
	s.UpsertLease(l)
	return nil
}

func (s *serializedStore) AllLeases() []*Lease {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*Lease, 0, len(s.leases))
	for _, l := range s.leases {
		result = append(result, l)
	}
	return result
}
