package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"hpc101-platform/controller"
)

func main() {
	endpoint := os.Getenv("HPC101_RUNTIME_ENDPOINT")
	if endpoint == "" {
		log.Fatal("HPC101_RUNTIME_ENDPOINT required (e.g. tcp://podman-runtime.hpc101-runtime:2375)")
	}
	rt, err := newRuntimeAdapter(endpoint)
	if err != nil {
		log.Fatalf("controller: runtime adapter: %v", err)
	}
	sub := newSubmissionService()
	store := controller.NewSerializedStore()
	drainer := &controller.NoopBastionDrainer{}
	h := controller.NewHandlerWithDrainer(store, rt, sub, drainer)

	interval := 30 * time.Second
	if v := os.Getenv("HPC101_RELEASE_TRIGGER_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}
	controller.StartReleaseTriggers(context.Background(), store, rt, drainer, interval)

	log.Println("controller listening on :8080")
	if err := http.ListenAndServe(":8080", h); err != nil {
		log.Fatalf("controller: %v", err)
	}
}
