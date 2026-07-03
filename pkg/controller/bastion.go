package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// BastionSession represents one active SSH session/channel as reported
// by the bastion host to the controller.
type BastionSession struct {
	Principal    string `json:"principal"`
	ChannelCount int    `json:"channel_count"`
	LastActive   string `json:"last_active"`
}

// BastionRoster is a batch of active sessions reported periodically.
type BastionRoster struct {
	Hostname  string           `json:"hostname"`
	Timestamp string           `json:"timestamp"`
	Sessions  []BastionSession `json:"sessions"`
}

// handleBastionRoster handles POST /api/v1/bastion/roster.
// The bastion periodically POSTs its active session list. The controller
// reconciles this with lease state to update LastSeenAt and
// ActiveChannelCount for idle detection.
func (h *Handler) handleBastionRoster(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var roster BastionRoster
	if err := json.NewDecoder(r.Body).Decode(&roster); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	seen := map[string]bool{}
	now := time.Now()
	for _, sess := range roster.Sessions {
		if sess.Principal == "" {
			continue
		}
		// Fetch current lease and update activity tracking.
		l, err := h.store.LookupByPrincipal(sess.Principal)
		if err != nil || l == nil || l.State != "Active" {
			continue
		}
		l.ActiveChannelCount = sess.ChannelCount
		if sess.ChannelCount > 0 {
			l.LastSeenAt = now
		}
		// Parse reported timestamp if provided.
		if sess.LastActive != "" {
			if t, err := time.Parse(time.RFC3339, sess.LastActive); err == nil {
				if t.After(l.LastSeenAt) {
					l.LastSeenAt = t
				}
			}
		}
		seen[sess.Principal] = true
		_ = h.store.UpsertLease(l)
	}

	// Diff: zero active channel counts for active leases NOT in this roster.
	if lister, ok := h.store.(LeaseStoreWithList); ok {
		for _, l := range lister.AllLeases() {
			if l.State != "Active" || seen[l.Owner] {
				continue
			}
			l.ActiveChannelCount = 0
			_ = h.store.UpsertLease(l)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// HTTPBastionDrainer implements BastionDrainer by calling the bastion's
// internal API to terminate forwarding channels and reject new connections.
// This replaces the NoopBastionDrainer in production.
type HTTPBastionDrainer struct {
	BastionURL string
	client     *http.Client
}

// NewHTTPBastionDrainer creates a drainer that calls the bastion's drain API.
// bastionURL is the internal HTTP endpoint of the bastion management API,
// e.g. "http://bastion.hpc101-bastion.svc.cluster.local:8080".
func NewHTTPBastionDrainer(bastionURL string) *HTTPBastionDrainer {
	return &HTTPBastionDrainer{
		BastionURL: bastionURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Drain calls the bastion to terminate all channels for the given principal
// and reject new SSH connections for them.
func (d *HTTPBastionDrainer) Drain(principal string) error {
	if d.BastionURL == "" {
		return fmt.Errorf("bastion drain: no bastion URL configured")
	}
	payload, _ := json.Marshal(map[string]string{"principal": principal})
	url := d.BastionURL + "/api/v1/drain"
	resp, err := d.client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("bastion drain: POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("bastion drain: HTTP %d", resp.StatusCode)
	}
	return nil
}
