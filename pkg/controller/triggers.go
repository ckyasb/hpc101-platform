package controller

import (
	"context"
	"log"
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

func StartReleaseTriggers(ctx context.Context, store ReleaseOps, rt ContainerCreator, drainer BastionDrainer, interval time.Duration) {
	go runMaxLifeTrigger(ctx, store, rt, drainer, interval)
	go runIdleTrigger(ctx, store, rt, drainer, interval)
}

func runMaxLifeTrigger(ctx context.Context, store ReleaseOps, rt ContainerCreator, drainer BastionDrainer, interval time.Duration) {
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
				err := store.ReleaseLeaseIf(l.Owner, lease.TriggerMaxLife, func(l *Lease) bool { return l.IsExpired() }, rt, drainer)
				if err != nil {
					log.Printf("max-life release failed for %s: %v", l.Owner, err)
				}
			}
		}
	}
}

func runIdleTrigger(ctx context.Context, store ReleaseOps, rt ContainerCreator, drainer BastionDrainer, interval time.Duration) {
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
				err := store.ReleaseLeaseIf(l.Owner, lease.TriggerIdle, func(l *Lease) bool { return l.IsIdle() }, rt, drainer)
				if err != nil {
					log.Printf("idle release failed for %s: %v", l.Owner, err)
				}
			}
		}
	}
}
