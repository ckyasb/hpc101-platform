package controller

import (
	"context"
	"time"

	"hpc101-platform/lease"
)

type LeaseStoreWithList interface {
	LeaseStore
	AllLeases() []*Lease
}

func StartReleaseTriggers(ctx context.Context, store LeaseStoreWithList, rt ContainerCreator, interval time.Duration) {
	go runMaxLifeTrigger(ctx, store, rt, interval)
	go runIdleTrigger(ctx, store, rt, interval)
}

func runMaxLifeTrigger(ctx context.Context, store LeaseStoreWithList, rt ContainerCreator, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, l := range store.AllLeases() {
				if l.State != lease.StateActive || !l.IsExpired() {
					continue
				}
				releaseWithRecovery(l, lease.TriggerMaxLife, rt, store)
			}
		}
	}
}

func runIdleTrigger(ctx context.Context, store LeaseStoreWithList, rt ContainerCreator, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, l := range store.AllLeases() {
				if l.State != lease.StateActive || !l.IsIdle() {
					continue
				}
				releaseWithRecovery(l, lease.TriggerIdle, rt, store)
			}
		}
	}
}

func releaseWithRecovery(l *Lease, trigger lease.Trigger, rt ContainerCreator, store LeaseStore) {
	if !l.Release(trigger) {
		return
	}
	if err := l.ExecuteRelease(func(state lease.ReleaseState) error {
		if state == lease.StateStopped && rt != nil {
			return rt.StopService(l.ContainerID)
		}
		return nil
	}); err != nil {
		l.State = lease.StateActive
		l.ReleasedBy = ""
		l.ReleasedAt = time.Time{}
		store.UpsertLease(l)
		return
	}
	store.UpsertLease(l)
}
