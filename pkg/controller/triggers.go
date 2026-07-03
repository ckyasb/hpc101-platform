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

type ReleaseOps interface {
	LeaseStore
	AllLeases() []*Lease
	ReleaseLeaseIf(principal string, trigger lease.Trigger, shouldRelease func(*Lease) bool, rt ContainerCreator, drainer BastionDrainer) error
}

func StartReleaseTriggers(ctx context.Context, store ReleaseOps, rt ContainerCreator, interval time.Duration) {
	go runMaxLifeTrigger(ctx, store, rt, interval)
	go runIdleTrigger(ctx, store, rt, interval)
}

func runMaxLifeTrigger(ctx context.Context, store ReleaseOps, rt ContainerCreator, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Iterate copies; ReleaseLeaseIf rechecks under lock
			for _, l := range store.AllLeases() {
				if l.State != lease.StateActive || !l.IsExpired() {
					continue
				}
				store.ReleaseLeaseIf(l.Owner, lease.TriggerMaxLife,
					func(l *Lease) bool { return l.IsExpired() },
					rt, nil)
			}
		}
	}
}

func runIdleTrigger(ctx context.Context, store ReleaseOps, rt ContainerCreator, interval time.Duration) {
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
				store.ReleaseLeaseIf(l.Owner, lease.TriggerIdle,
					func(l *Lease) bool { return l.IsIdle() },
					rt, nil)
			}
		}
	}
}
