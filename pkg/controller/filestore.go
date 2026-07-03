// File: pkg/controller/filestore.go
// Package: controller
// Created: 2026-07-03
// Author: hpc101-platform contributors
// Purpose: JSON-file-backed persistent store for the hpc101-platform controller.
//          Implements LeaseStore, KeyStore, problem mapping, submission
//          persistence, and ReleaseOps so it can replace serializedStore in
//          production deployments that require state to survive restart.
// Change Summary: Atomic snapshot writes (tmp+rename), load-on-start,
//                 rollback on save failure for MapProblem.

package controller

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"hpc101-platform/lease"
)

// FileStore is a JSON-file-backed persistent store for leases, keys,
// submissions, and problem mappings. It implements LeaseStore, KeyStore,
// and problem mapping interfaces so it can replace serializedStore in
// production deployments.
//
// Atomicity: writes use a temporary file + rename pattern to prevent
// corruption on crash. Reads parse the full JSON snapshot on Load().
type FileStore struct {
	mu   sync.Mutex
	path string
	data fileStoreData
}

type fileStoreData struct {
	Leases      map[string]*Lease            `json:"leases"`
	Keys        map[string]string            `json:"keys"`
	Submissions map[string]*SubmissionRecord `json:"submissions"`
	ProblemMap  map[string]string            `json:"problem_map"`
}

// NewFileStore creates or loads a file-backed persistent store.
// If the file exists, Load() is called automatically to restore state.
func NewFileStore(path string) (*FileStore, error) {
	fs := &FileStore{
		path: path,
		data: fileStoreData{
			Leases:      make(map[string]*Lease),
			Keys:        make(map[string]string),
			Submissions: make(map[string]*SubmissionRecord),
			ProblemMap:  make(map[string]string),
		},
	}
	if _, err := os.Stat(path); err == nil {
		if err := fs.Load(); err != nil {
			return nil, fmt.Errorf("filestore: load %s: %w", path, err)
		}
	}
	return fs, nil
}

// Load reads the JSON snapshot from disk.
func (fs *FileStore) Load() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	data, err := os.ReadFile(fs.path)
	if err != nil {
		return fmt.Errorf("filestore: read: %w", err)
	}
	var loaded fileStoreData
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("filestore: parse: %w", err)
	}
	if loaded.Leases != nil {
		fs.data.Leases = loaded.Leases
	}
	if loaded.Keys != nil {
		fs.data.Keys = loaded.Keys
	}
	if loaded.Submissions != nil {
		fs.data.Submissions = loaded.Submissions
	}
	if loaded.ProblemMap != nil {
		fs.data.ProblemMap = loaded.ProblemMap
	}
	return nil
}

// Save writes the current state as an atomic JSON snapshot.
func (fs *FileStore) Save() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	data, err := json.MarshalIndent(fs.data, "", "  ")
	if err != nil {
		return fmt.Errorf("filestore: marshal: %w", err)
	}
	tmp := fs.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("filestore: write tmp: %w", err)
	}
	if err := os.Rename(tmp, fs.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("filestore: rename: %w", err)
	}
	return nil
}

// --- LeaseStore implementation ---

func (fs *FileStore) LookupByPrincipal(p string) (*Lease, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	l, ok := fs.data.Leases[p]
	if !ok || l == nil {
		return nil, nil
	}
	cpy := *l
	return &cpy, nil
}

func (fs *FileStore) UpsertLease(l *Lease) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	cpy := *l
	fs.data.Leases[l.Owner] = &cpy
	return fs.unsafeSaveLocked()
}

// AllLeases returns copies of all stored leases.
func (fs *FileStore) AllLeases() []*Lease {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	result := make([]*Lease, 0, len(fs.data.Leases))
	for _, l := range fs.data.Leases {
		cpy := *l
		result = append(result, &cpy)
	}
	return result
}

// --- KeyStore implementation ---

func (fs *FileStore) RegisterKey(principal, publicKey string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.data.Keys[principal] = publicKey
	return fs.unsafeSaveLocked()
}

func (fs *FileStore) GetKey(principal string) (string, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	k, ok := fs.data.Keys[principal]
	if !ok {
		return "", fmt.Errorf("no registered key for %s", principal)
	}
	return k, nil
}

// --- Problem mapping ---

func (fs *FileStore) MapProblem(course, contest, platformID, csojID string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	key := course + ":" + contest + ":" + platformID
	// Preserve previous value for rollback on save failure.
	prev, hadPrev := fs.data.ProblemMap[key]
	fs.data.ProblemMap[key] = csojID
	if err := fs.unsafeSaveLocked(); err != nil {
		// Roll back to previous state.
		if hadPrev {
			fs.data.ProblemMap[key] = prev
		} else {
			delete(fs.data.ProblemMap, key)
		}
		return err
	}
	return nil
}

func (fs *FileStore) ResolveProblem(course, contest, platformID string) string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.data.ProblemMap[course+":"+contest+":"+platformID]
}

// --- Submission persistence ---

func (fs *FileStore) SaveSubmission(rec *SubmissionRecord) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	cpy := *rec
	if len(rec.Result.Containers) > 0 {
		cpy.Result.Containers = make([]ContainerInfo, len(rec.Result.Containers))
		copy(cpy.Result.Containers, rec.Result.Containers)
	}
	fs.data.Submissions[rec.ID] = &cpy
	return fs.unsafeSaveLocked()
}

func (fs *FileStore) GetSubmission(id string) (*SubmissionRecord, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	r, ok := fs.data.Submissions[id]
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

func (fs *FileStore) AllSubmissions() []*SubmissionRecord {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	result := make([]*SubmissionRecord, 0, len(fs.data.Submissions))
	for _, r := range fs.data.Submissions {
		cpy := *r
		if len(r.Result.Containers) > 0 {
			cpy.Result.Containers = make([]ContainerInfo, len(r.Result.Containers))
			copy(cpy.Result.Containers, r.Result.Containers)
		}
		result = append(result, &cpy)
	}
	return result
}

// unsafeSaveLocked saves without acquiring the lock (caller must hold fs.mu).
func (fs *FileStore) unsafeSaveLocked() error {
	data, err := json.MarshalIndent(fs.data, "", "  ")
	if err != nil {
		return fmt.Errorf("filestore: marshal: %w", err)
	}
	tmp := fs.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("filestore: write tmp: %w", err)
	}
	if err := os.Rename(tmp, fs.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("filestore: rename: %w", err)
	}
	return nil
}

// ReleaseLeaseIf implements the ReleaseOps interface for atomic release
// lifecycle. It holds the store lock for the full lifecycle (including
// drain and stop) to serialize release vs concurrent up/trigger.
func (fs *FileStore) ReleaseLeaseIf(principal string, trigger lease.Trigger, shouldRelease func(*Lease) bool, rt ContainerCreator, drainer BastionDrainer) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	l, ok := fs.data.Leases[principal]
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
		fs.data.Leases[principal] = l
		_ = fs.unsafeSaveLocked()
		return err
	}
	fs.data.Leases[principal] = l
	return fs.unsafeSaveLocked()
}
