// Package controller provides the hpc101-platform controller HTTP API.
// Currently implements the lease lookup endpoint consumed by the
// bastion's AuthorizedPrincipalsCommand.
package controller

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"hpc101-platform/lease"
)

// LeaseStore is the interface for lease storage — implemented by
// the full lease repository (task11).
type LeaseStore interface {
	LookupByPrincipal(principal string) (*lease.Lease, error)
}

// Handler serves the controller HTTP API.
type Handler struct {
	store LeaseStore
	mux   *http.ServeMux
}

// NewHandler creates a controller API handler.
func NewHandler(store LeaseStore) *Handler {
	h := &Handler{store: store, mux: http.NewServeMux()}
	h.mux.HandleFunc("/api/v1/leases", h.handleLeases)
	return h
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// principalPattern validates principal names (alphanumeric, hyphen, underscore).
var principalPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func (h *Handler) handleLeases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	principal := r.URL.Query().Get("principal")
	if principal == "" {
		http.Error(w, `{"error":"missing principal"}`, http.StatusBadRequest)
		return
	}

	// Validate principal — reject injection attempts
	if !principalPattern.MatchString(principal) {
		http.Error(w, `{"error":"invalid principal"}`, http.StatusBadRequest)
		return
	}

	l, err := h.store.LookupByPrincipal(principal)
	if err != nil {
		http.Error(w, `{"error":"lease lookup failed"}`, http.StatusInternalServerError)
		return
	}
	if l == nil || l.State != lease.StateActive {
		http.Error(w, `{"error":"no active lease"}`, http.StatusNotFound)
		return
	}

	// Only return active leases with valid host/port
	host := strings.TrimSpace(l.Host)
	if host == "" || l.Port == 0 || l.Port > 65535 {
		http.Error(w, `{"error":"invalid lease endpoint"}`, http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"container_host": host,
		"container_port": l.Port,
		"state":          string(l.State),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
