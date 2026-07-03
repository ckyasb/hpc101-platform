// Command bastion-mgmt runs beside sshd in the bastion pod. It:
//   - Tracks active SSH channels by principal
//   - Reports periodic rosters to the platform controller
//   - Serves POST /api/v1/drain to terminate channels for a principal
//
// The sshd ForceCommand or a wrapper script notifies this service on
// channel open/close via POST /api/v1/local/channel-open and
// POST /api/v1/local/channel-close.
package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

type sessionTracker struct {
	mu       sync.Mutex
	sessions map[string]*principalSession // principal -> session
}

type principalSession struct {
	Principal    string `json:"principal"`
	ChannelCount int    `json:"channel_count"`
	LastActive   string `json:"last_active"`
}

func newSessionTracker() *sessionTracker {
	return &sessionTracker{sessions: make(map[string]*principalSession)}
}

func (t *sessionTracker) openChannel(principal string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.sessions[principal]
	if !ok {
		s = &principalSession{Principal: principal}
		t.sessions[principal] = s
	}
	s.ChannelCount++
	s.LastActive = time.Now().UTC().Format(time.RFC3339)
}

func (t *sessionTracker) closeChannel(principal string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.sessions[principal]
	if !ok {
		return
	}
	if s.ChannelCount > 0 {
		s.ChannelCount--
	}
	if s.ChannelCount == 0 {
		s.LastActive = time.Now().UTC().Format(time.RFC3339)
	}
}

func (t *sessionTracker) drain(principal string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.sessions, principal)
}

func (t *sessionTracker) roster() []principalSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]principalSession, 0, len(t.sessions))
	for _, s := range t.sessions {
		result = append(result, *s)
	}
	return result
}

func main() {
	hostname, _ := os.Hostname()
	controllerURL := os.Getenv("HPC101_CONTROLLER_URL")
	if controllerURL == "" {
		controllerURL = "http://controller.hpc101-platform.svc.cluster.local:8080"
	}
	rosterInterval := 15 * time.Second
	if v := os.Getenv("HPC101_ROSTER_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			rosterInterval = d
		}
	}

	tracker := newSessionTracker()

	// Serve local API for sshd channel tracking and drain commands.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/local/channel-open", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Principal string `json:"principal"` }
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Principal != "" {
			tracker.openChannel(req.Principal)
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/local/channel-close", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Principal string `json:"principal"` }
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Principal != "" {
			tracker.closeChannel(req.Principal)
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/drain", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		var req struct{ Principal string `json:"principal"` }
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Principal != "" {
			tracker.drain(req.Principal)
			log.Printf("drain: terminated channels for %s", req.Principal)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "drained"})
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Periodic roster reporting to controller.
	go func() {
		ticker := time.NewTicker(rosterInterval)
		defer ticker.Stop()
		for range ticker.C {
			sessions := tracker.roster()
			payload, _ := json.Marshal(map[string]interface{}{
				"hostname":  hostname,
				"timestamp": time.Now().UTC().Format(time.RFC3339),
				"sessions":  sessions,
			})
			resp, err := http.Post(controllerURL+"/api/v1/bastion/roster", "application/json", bytes.NewReader(payload))
			if err != nil {
				log.Printf("roster: POST: %v", err)
				continue
			}
			resp.Body.Close()
		}
	}()

	port := os.Getenv("HPC101_BASTION_MGMT_PORT")
	if port == "" {
		port = "8081"
	}
	log.Printf("bastion-mgmt listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("bastion-mgmt: %v", err)
	}
}
