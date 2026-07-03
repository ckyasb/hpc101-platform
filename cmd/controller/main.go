package main

import (
	"log"
	"net/http"
	"os"

	"hpc101-platform/controller"
)

// memStore is a minimal in-memory lease store for development.
// Production replaces this with a persisted repository (task11).
type memStore map[string]*controller.Lease

func (m memStore) LookupByPrincipal(p string) (*controller.Lease, error) {
	return m[p], nil
}
func (m memStore) UpsertLease(l *controller.Lease) error {
	m[l.Owner] = l
	return nil
}

func main() {
	endpoint := os.Getenv("HPC101_RUNTIME_ENDPOINT")
	if endpoint == "" {
		log.Fatal("HPC101_RUNTIME_ENDPOINT required (e.g. tcp://podman-runtime.hpc101-runtime:2375)")
	}
	rt, err := newRuntimeAdapter(endpoint)
	if err != nil {
		log.Fatalf("controller: runtime adapter: %v", err)
	}
	h := controller.NewHandler(memStore{}, rt, nil)
	log.Println("controller listening on :8080")
	if err := http.ListenAndServe(":8080", h); err != nil {
		log.Fatalf("controller: %v", err)
	}
}
