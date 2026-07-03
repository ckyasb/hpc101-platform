// Package controller provides the hpc101-platform controller HTTP API.
// Currently implements the lease lookup endpoint consumed by the
// bastion's AuthorizedPrincipalsCommand.
package controller

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"hpc101-platform/lease"
)

// Lease type alias for callers importing this package.
type Lease = lease.Lease

// LeaseStore is the interface for lease storage — implemented by
// the full lease repository (task11).
type LeaseStore interface {
	LookupByPrincipal(principal string) (*Lease, error)
	UpsertLease(l *Lease) error
}

// CreateServiceRequest carries the parameters to create a student service.
type CreateServiceRequest struct {
	Principal string `json:"principal"`
	Image     string `json:"image"`
	SSHKey    string `json:"ssh_key"`
	Course    string `json:"course"`
	Problem   string `json:"problem"`
	CPULimit  int64  `json:"cpu_limit"`
	MemoryMB  int64  `json:"memory_mb"`
}

// ServiceResult is returned when a service container is created and started.
type ServiceResult struct {
	ContainerID string
	Host        string
	Port        uint16
}

// ContainerCreator is the interface for creating service containers.
type ContainerCreator interface {
	CreateService(req CreateServiceRequest) (*ServiceResult, error)
}

// SubmissionService is the interface for submitting solutions for judging.
type SubmissionService interface {
	Submit(problemID string, files map[string][]byte) (string, error)
}

// Handler serves the controller HTTP API.
type Handler struct {
	store      LeaseStore
	runtime    ContainerCreator
	submission SubmissionService
	mux        *http.ServeMux
}

// NewHandler creates a controller API handler.
func NewHandler(store LeaseStore, runtime ContainerCreator, submission SubmissionService) *Handler {
	h := &Handler{store: store, runtime: runtime, submission: submission, mux: http.NewServeMux()}
	h.mux.HandleFunc("/api/v1/leases", h.handleLeases)
	h.mux.HandleFunc("/api/v1/services", h.handleCreateService)
	h.mux.HandleFunc("/api/v1/release", h.handleRelease)
	h.mux.HandleFunc("/api/v1/problems", h.handleProblems)
	h.mux.HandleFunc("/api/v1/scores", h.handleScores)
	h.mux.HandleFunc("/api/v1/submissions", h.handleSubmissions)
	h.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
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

// handleCreateService handles POST /api/v1/services — create a student service container.
func (h *Handler) handleCreateService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if h.runtime == nil {
		http.Error(w, `{"error":"runtime not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var req CreateServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if !principalPattern.MatchString(req.Principal) {
		http.Error(w, `{"error":"invalid principal"}`, http.StatusBadRequest)
		return
	}
	if req.Image == "" || req.SSHKey == "" || req.Course == "" || req.Problem == "" {
		http.Error(w, `{"error":"principal, image, ssh_key, course, and problem are required"}`, http.StatusBadRequest)
		return
	}

	result, err := h.runtime.CreateService(req)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	l := lease.NewLease(req.Principal, result.ContainerID,
		"svc-"+req.Principal, result.Host, result.Port, 8*time.Hour, 30*time.Minute)
	if err := h.store.UpsertLease(l); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"container_id": result.ContainerID,
		"host":         result.Host,
		"port":         result.Port,
		"state":        string(l.State),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	principal := r.URL.Query().Get("principal")
	if !principalPattern.MatchString(principal) {
		http.Error(w, `{"error":"invalid principal"}`, http.StatusBadRequest)
		return
	}
	l, err := h.store.LookupByPrincipal(principal)
	if err != nil || l == nil {
		http.Error(w, `{"error":"no active lease"}`, http.StatusNotFound)
		return
	}
	if err := l.ExecuteRelease(func(s lease.ReleaseState) error { return nil }); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if err := h.store.UpsertLease(l); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": string(l.State)})
}

func (h *Handler) handleProblems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"problems": []interface{}{}})
}

func (h *Handler) handleScores(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"scores": []interface{}{}})
}

func (h *Handler) handleSubmissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if h.submission == nil {
		http.Error(w, `{"error":"submission service not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var req struct {
		ProblemID string            `json:"problem_id"`
		Files     map[string]string `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if req.ProblemID == "" || len(req.Files) == 0 {
		http.Error(w, `{"error":"problem_id and files are required"}`, http.StatusBadRequest)
		return
	}
	files := make(map[string][]byte)
	for name, b64 := range req.Files {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"bad base64 for %s"}`, name), http.StatusBadRequest)
			return
		}
		files[name] = data
	}
	id, err := h.submission.Submit(req.ProblemID, files)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"submission_id": id, "status": "submitted"})
}
