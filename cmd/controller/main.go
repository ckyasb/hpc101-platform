package main

import (
	"log"
	"net/http"

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
	h := controller.NewHandler(memStore{}, nil) // runtime set in deploy (requires go >=1.25)
	log.Println("controller listening on :8080")
	if err := http.ListenAndServe(":8080", h); err != nil {
		log.Fatalf("controller: %v", err)
	}
}
