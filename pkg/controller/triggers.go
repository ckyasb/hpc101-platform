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
				if l.State == lease.StateActive && l.IsExpired() {
					l.Release(lease.TriggerMaxLife)
					l.ExecuteRelease(func(state lease.ReleaseState) error {
						if state == lease.StateStopped && rt != nil {
							return rt.StopService(l.ContainerID)
						}
						return nil
					})
					store.UpsertLease(l)
				}
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
				if l.State == lease.StateActive && l.IsIdle() {
					l.Release(lease.TriggerIdle)
					l.ExecuteRelease(func(state lease.ReleaseState) error {
						if state == lease.StateStopped && rt != nil {
							return rt.StopService(l.ContainerID)
						}
						return nil
					})
					store.UpsertLease(l)
				}
			}
		}
	}
}
