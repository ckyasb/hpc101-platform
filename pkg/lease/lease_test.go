package lease

import (
	"testing"
	"time"
)

func TestNewLease(t *testing.T) {
	l := NewLease("student-42", "abc123", "svc-student-42", "10.0.0.5", 2222,
		8*time.Hour, 30*time.Minute)

	if l.Owner != "student-42" {
		t.Errorf("Owner: got %q", l.Owner)
	}
	if l.Port != 2222 {
		t.Errorf("Port: got %d", l.Port)
	}
	if l.State != StateActive {
		t.Errorf("State: got %v", l.State)
	}
	if l.DeadlineAt.IsZero() {
		t.Error("DeadlineAt should not be zero when maxLife > 0")
	}
	if l.LastSeenAt.IsZero() {
		t.Error("LastSeenAt should be set")
	}
}

func TestNewLeaseNoMaxLife(t *testing.T) {
	l := NewLease("alice", "def456", "svc-alice", "10.0.0.6", 2222, 0, 15*time.Minute)
	if !l.DeadlineAt.IsZero() {
		t.Error("DeadlineAt should be zero when maxLife is 0")
	}
}

func TestReleaseFlow(t *testing.T) {
	l := NewLease("bob", "ghi789", "svc-bob", "10.0.0.7", 2222,
		1*time.Hour, 10*time.Minute)

	// Release
	if !l.Release(TriggerManual) {
		t.Fatal("Release should succeed from Active")
	}
	if l.State != StateClosing {
		t.Errorf("after release: got %v, want Closing", l.State)
	}
	if l.ReleasedBy != TriggerManual {
		t.Errorf("ReleasedBy: got %v", l.ReleasedBy)
	}
	if l.ReleasedAt.IsZero() {
		t.Error("ReleasedAt should be set")
	}

	// Cannot release again
	if l.Release(TriggerMaxLife) {
		t.Error("Release should fail from non-Active state")
	}

	// Drain
	if !l.Transition(StateDraining) {
		t.Error("Closing->Draining should succeed")
	}
	// Stop
	if !l.Transition(StateStopped) {
		t.Error("Draining->Stopped should succeed")
	}
	// Reclaim
	if !l.Transition(StateReclaimed) {
		t.Error("Stopped->Reclaimed should succeed")
	}
	// Cannot transition further
	if l.Transition(StateActive) {
		t.Error("Reclaimed->Active should fail")
	}
}

func TestIsExpired(t *testing.T) {
	l := NewLease("eve", "jkl", "svc-eve", "10.0.0.8", 2222,
		1*time.Nanosecond, 5*time.Minute)
	time.Sleep(10 * time.Millisecond)
	if !l.IsExpired() {
		t.Error("lease with 1ns maxLife should be expired immediately")
	}
}

func TestIsIdle(t *testing.T) {
	l := NewLease("mallory", "mno", "svc-mallory", "10.0.0.9", 2222,
		8*time.Hour, 1*time.Nanosecond)
	time.Sleep(10 * time.Millisecond)
	if !l.IsIdle() {
		t.Error("lease with 0 channels and 1ns idle timeout should be idle")
	}
	l.ActiveChannelCount = 1
	if l.IsIdle() {
		t.Error("lease with active channels should not be idle")
	}
}

func TestIsTerminal(t *testing.T) {
	l := NewLease("trent", "pqr", "svc-trent", "10.0.0.10", 2222,
		8*time.Hour, 30*time.Minute)
	if l.IsTerminal() {
		t.Error("Active lease should not be terminal")
	}
	l.Release(TriggerManual)
	l.Transition(StateDraining)
	l.Transition(StateStopped)
	l.Transition(StateReclaimed)
	if !l.IsTerminal() {
		t.Error("Reclaimed lease should be terminal")
	}
}

func TestExecuteRelease(t *testing.T) {
	l := NewLease("eve", "ctr-eve", "svc-eve", "10.0.0.5", 2222, 8*time.Hour, 30*time.Minute)
	var steps []ReleaseState
	err := l.ExecuteRelease(func(s ReleaseState) error {
		steps = append(steps, s)
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteRelease: %v", err)
	}
	expected := []ReleaseState{StateClosing, StateDraining, StateStopped, StateReclaimed}
	for i, s := range expected {
		if i >= len(steps) || steps[i] != s {
			t.Errorf("step %d: expected %s, got %v", i, s, steps)
		}
	}
	if l.State != StateReclaimed {
		t.Errorf("final state: %s", l.State)
	}
}

func TestExecuteReleaseReleasesOnlyOnce(t *testing.T) {
	l := NewLease("frank", "ctr-frank", "svc-frank", "10.0.0.6", 2222, 8*time.Hour, 30*time.Minute)
	if !l.Release(TriggerManual) {
		t.Fatal("first release should succeed")
	}
	if l.Release(TriggerIdle) {
		t.Error("second release should fail")
	}
}

func TestReleaseAllTriggers(t *testing.T) {
	for _, trig := range []Trigger{TriggerManual, TriggerMaxLife, TriggerIdle} {
		l := NewLease("test", "c", "svc-test", "10.0.0.7", 2222, 8*time.Hour, 30*time.Minute)
		if !l.Release(trig) {
			t.Errorf("release with %s should succeed", trig)
		}
		if l.ReleasedBy != trig {
			t.Errorf("ReleasedBy: got %s, want %s", l.ReleasedBy, trig)
		}
	}
}
