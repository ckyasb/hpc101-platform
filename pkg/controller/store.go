package controller

import (
	"fmt"
	"sync"

	"hpc101-platform/lease"
)

type serializedStore struct {
	mu          sync.Mutex
	leases      map[string]*Lease
	keys        map[string]string            // principal → public key
	submissions map[string]*SubmissionRecord // submissionID → record
	problemMap  map[string]string            // "course:contest:problem" → csojProblemID
}

func NewSerializedStore() *serializedStore {
	return &serializedStore{
		leases:      make(map[string]*Lease),
		keys:        make(map[string]string),
		submissions: make(map[string]*SubmissionRecord),
		problemMap:  make(map[string]string),
	}
}

// RegisterKey implements KeyStore — persists a public key in the store.
func (s *serializedStore) RegisterKey(principal, publicKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[principal] = publicKey
	return nil
}

// GetKey implements KeyStore — retrieves a persisted public key.
func (s *serializedStore) GetKey(principal string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[principal]
	if !ok {
		return "", fmt.Errorf("no registered key for %s", principal)
	}
	return k, nil
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

// SaveSubmission persists a submission record in the store.
func (s *serializedStore) SaveSubmission(rec *SubmissionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cpy := *rec
	if len(rec.Result.Containers) > 0 {
		cpy.Result.Containers = make([]ContainerInfo, len(rec.Result.Containers))
		copy(cpy.Result.Containers, rec.Result.Containers)
	}
	s.submissions[rec.ID] = &cpy
	return nil
}

// GetSubmission retrieves a persisted submission record.
func (s *serializedStore) GetSubmission(id string) (*SubmissionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.submissions[id]
	if !ok {
		return nil, fmt.Errorf("submission %s not found", id)
	}
	cpy := *r
	if len(r.Result.Containers) > 0 {
		cpy.Result.Containers = make([]ContainerInfo, len(r.Result.Containers))
		copy(cpy.Result.Containers, r.Result.Containers)
	}
	return &cpy, nil
}

// AllSubmissions returns all persisted submission records.
func (s *serializedStore) AllSubmissions() []*SubmissionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*SubmissionRecord, 0, len(s.submissions))
	for _, r := range s.submissions {
		cpy := *r
		if len(r.Result.Containers) > 0 {
			cpy.Result.Containers = make([]ContainerInfo, len(r.Result.Containers))
			copy(cpy.Result.Containers, r.Result.Containers)
		}
		result = append(result, &cpy)
	}
	return result
}

// MapProblem stores the CSOJ problem ID for a course+contest+platform problem.
func (s *serializedStore) MapProblem(course, contest, platformID, csojID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.problemMap[course+":"+contest+":"+platformID] = csojID
}

// ResolveProblem returns the mapped CSOJ problem ID, or empty if not found.
func (s *serializedStore) ResolveProblem(course, contest, platformID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.problemMap[course+":"+contest+":"+platformID]
}
