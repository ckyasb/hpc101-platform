// Package lease defines the lease state model for hpc101-platform.
// A Lease tracks a student's active service container — its identity,
// lifecycle state, network endpoint, time budget, and SSH session activity.
//
// This is the core data model for AC-3 (idle tracking), AC-4 (auto-close
// release state machine), and AC-5 (restart/orphan recovery).
package lease

import (
	"fmt"
	"time"
)

// ReleaseState enumerates the lifecycle phases of a service lease.
type ReleaseState string

const (
	// Active — container is running and accepting SSH connections.
	StateActive ReleaseState = "Active"

	// Closing — release triggered (manual/expiry/idle), rejecting new SSH.
	StateClosing ReleaseState = "Closing"

	// Draining — terminating active bastion channels, waiting for drain.
	StateDraining ReleaseState = "Draining"

	// Stopped — container stopped, volumes still present.
	StateStopped ReleaseState = "Stopped"

	// Reclaimed — volumes and networks removed, lease archived.
	StateReclaimed ReleaseState = "Reclaimed"
)

// validTransitions defines the allowed state transitions.
var validTransitions = map[ReleaseState][]ReleaseState{
	StateActive:    {StateClosing},
	StateClosing:   {StateDraining},
	StateDraining:  {StateStopped},
	StateStopped:   {StateReclaimed},
	StateReclaimed: {},
}

// CanTransition returns true if moving from current to next is legal.
func CanTransition(current, next ReleaseState) bool {
	for _, allowed := range validTransitions[current] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Trigger indicates what caused a release.
type Trigger string

const (
	TriggerManual    Trigger = "manual"
	TriggerMaxLife   Trigger = "max_life"
	TriggerIdle      Trigger = "idle"
)

// Lease tracks a single student service container.
type Lease struct {
	// Owner is the student principal (from SSH cert KeyId).
	Owner string `json:"owner"`

	// ContainerID is the podman container ID.
	ContainerID string `json:"container_id"`

	// ContainerName is the podman container name (svc- or csj- prefixed).
	ContainerName string `json:"container_name"`

	// Host is the container's reachable hostname or IP.
	Host string `json:"host"`

	// Port is the SSH port on the container.
	Port uint16 `json:"port"`

	// StartedAt is when the container was created.
	StartedAt time.Time `json:"started_at"`

	// DeadlineAt is the max-lifetime expiry (zero = no limit).
	DeadlineAt time.Time `json:"deadline_at"`

	// IdleTimeout is the duration after which inactivity triggers release.
	IdleTimeout time.Duration `json:"idle_timeout"`

	// LastSeenAt is the last time SSH channel activity was observed.
	LastSeenAt time.Time `json:"last_seen_at"`

	// ActiveChannelCount is the current number of open SSH channels.
	ActiveChannelCount int `json:"active_channel_count"`

	// State is the current lifecycle state.
	State ReleaseState `json:"state"`

	// ReleasedBy records which trigger initiated the release (empty if active).
	ReleasedBy Trigger `json:"released_by,omitempty"`

	// ReleasedAt is when the release was triggered (zero if active).
	ReleasedAt time.Time `json:"released_at,omitempty"`
}

// NewLease creates a new Active lease with the given parameters.
func NewLease(owner, containerID, containerName, host string, port uint16, maxLife, idleTimeout time.Duration) *Lease {
	now := time.Now()
	var deadline time.Time
	if maxLife > 0 {
		deadline = now.Add(maxLife)
	}
	return &Lease{
		Owner:              owner,
		ContainerID:        containerID,
		ContainerName:      containerName,
		Host:               host,
		Port:               port,
		StartedAt:          now,
		DeadlineAt:         deadline,
		IdleTimeout:        idleTimeout,
		LastSeenAt:         now,
		ActiveChannelCount: 0,
		State:              StateActive,
	}
}

// IsExpired returns true if the max-lifetime deadline has passed.
func (l *Lease) IsExpired() bool {
	if l.DeadlineAt.IsZero() {
		return false
	}
	return time.Now().After(l.DeadlineAt)
}

// IsIdle returns true if no channel activity has occurred within the idle timeout.
func (l *Lease) IsIdle() bool {
	if l.IdleTimeout <= 0 {
		return false
	}
	return l.ActiveChannelCount == 0 && time.Since(l.LastSeenAt) > l.IdleTimeout
}

// IsTerminal returns true if the lease has reached a terminal state.
func (l *Lease) IsTerminal() bool {
	return l.State == StateReclaimed
}

// Transition attempts to move the lease to the next state.
// Returns false if the transition is not allowed.
func (l *Lease) Transition(next ReleaseState) bool {
	if !CanTransition(l.State, next) {
		return false
	}
	l.State = next
	return true
}

// Release initiates the release sequence with the given trigger.
// Returns false if the lease is already in a non-Active state.
func (l *Lease) Release(trigger Trigger) bool {
	if l.State != StateActive {
		return false
	}
	l.ReleasedBy = trigger
	l.ReleasedAt = time.Now()
	return l.Transition(StateClosing)
}

// ExecuteRelease runs the full release lifecycle from the current state.
// Calls callback before each transition. If callback returns an error,
// execution stops and the error is returned.
// Returns nil when the lease reaches Reclaimed.
func (l *Lease) ExecuteRelease(callback func(state ReleaseState) error) error {
	steps := []ReleaseState{StateClosing, StateDraining, StateStopped, StateReclaimed}
	for _, s := range steps {
		if l.State == s {
			continue // already at this state
		}
		if err := callback(s); err != nil {
			return fmt.Errorf("release step %s: %w", s, err)
		}
		if !l.Transition(s) {
			return fmt.Errorf("release: cannot transition from %s to %s", l.State, s)
		}
		if s == StateReclaimed {
			return nil
		}
	}
	return nil
}
