// Package controller provides the hpc101-platform controller HTTP API.
// Currently implements the lease lookup endpoint consumed by the
// bastion's AuthorizedPrincipalsCommand.
package controller

import (
	"context"
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

// ContainerCreator is the interface for creating and destroying service containers.
type ContainerCreator interface {
	CreateService(req CreateServiceRequest) (*ServiceResult, error)
	StopService(containerID string) error
}

// BastionDrainer is called during release to reject new SSH and terminate channels.
type BastionDrainer interface {
	Drain(principal string) error
}

// SubmissionService is the interface for submitting solutions for judging
// and querying results. The adapter implements both submission and result retrieval.
type SubmissionService interface {
	Submit(ctx context.Context, problemID string, files map[string][]byte) (string, error)
	QueryResult(ctx context.Context, submissionID string) (*SubmissionResult, error)
}

// SubmissionResult holds the judging outcome from CSOJ.
type SubmissionResult struct {
	SubmissionID string  `json:"submission_id"`
	ProblemID    string  `json:"problem_id"`
	Status       string  `json:"status"` // Queued, Running, Success, Failed
	Score        float64 `json:"score"`
	Performance  float64 `json:"performance"`
	Info         string  `json:"info,omitempty"`
}

// SubmissionRecord tracks a submission through its lifecycle.
type SubmissionRecord struct {
	ID        string          `json:"id"`
	ProblemID string          `json:"problem_id"`
	Principal string          `json:"principal"`
	Submitted string          `json:"submitted_at"`
	Result    SubmissionResult `json:"result,omitempty"`
}

// CertSigner signs short-lived SSH user certificates for bastion access.
// The signed cert binds a principal to a container target via the bastion's
// AuthorizedPrincipalsCommand (permitopen) and includes permit-port-forwarding.
type CertSigner interface {
	SignUserCert(publicKey string, principal string, validityHours int) (certPEM string, err error)
}

// RegisteredKey is a student's registered SSH public key.
type RegisteredKey struct {
	Principal string `json:"principal"`
	PublicKey string `json:"public_key"`
}

// Handler serves the controller HTTP API.
type Handler struct {
	drainer     BastionDrainer
	store       LeaseStore
	runtime     ContainerCreator
	submission  SubmissionService
	certSigner  CertSigner
	keys        map[string]string            // principal → public key (in-memory key store)
	submissions map[string]*SubmissionRecord // submissionID → record
	mux         *http.ServeMux
}

// NewHandler creates a controller API handler with no drainer or cert signer.
func NewHandler(store LeaseStore, runtime ContainerCreator, submission SubmissionService) *Handler {
	return newHandler(store, runtime, submission, nil, nil)
}

// NewHandlerWithCertSigner creates a handler with SSH cert signing capability.
func NewHandlerWithCertSigner(store LeaseStore, runtime ContainerCreator, submission SubmissionService, signer CertSigner) *Handler {
	return newHandler(store, runtime, submission, nil, signer)
}

// NewHandlerWithDrainer creates a handler with a bastion drainer.
func NewHandlerWithDrainer(store LeaseStore, runtime ContainerCreator, submission SubmissionService, drainer BastionDrainer) *Handler {
	return newHandler(store, runtime, submission, drainer, nil)
}

// NewHandlerWithDrainerAndSigner creates a handler with both drainer and cert signer.
func NewHandlerWithDrainerAndSigner(store LeaseStore, runtime ContainerCreator, submission SubmissionService, drainer BastionDrainer, signer CertSigner) *Handler {
	return newHandler(store, runtime, submission, drainer, signer)
}

func newHandler(store LeaseStore, runtime ContainerCreator, submission SubmissionService, drainer BastionDrainer, signer CertSigner) *Handler {
	h := &Handler{
		store:       store,
		runtime:     runtime,
		submission:  submission,
		drainer:     drainer,
		certSigner:  signer,
		keys:        make(map[string]string),
		submissions: make(map[string]*SubmissionRecord),
		mux:         http.NewServeMux(),
	}
	h.mux.HandleFunc("/api/v1/leases", h.handleLeases)
	h.mux.HandleFunc("/api/v1/services", h.handleCreateService)
	h.mux.HandleFunc("/api/v1/release", h.handleRelease)
	h.mux.HandleFunc("/api/v1/problems", h.handleProblems)
	h.mux.HandleFunc("/api/v1/scores", h.handleScores)
	h.mux.HandleFunc("/api/v1/submissions", h.handleSubmissions)
	h.mux.HandleFunc("/api/v1/submissions/", h.handleSubmissionByID)
	h.mux.HandleFunc("/api/v1/keys", h.handleKeys)
	h.mux.HandleFunc("/api/v1/ssh-info", h.handleSSHInfo)
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

	// Sign a short-lived SSH certificate if we have a CA signer and the
	// student has registered a public key.
	if h.certSigner != nil {
		pubKey, hasKey := h.keys[req.Principal]
		if hasKey {
			certPEM, err := h.certSigner.SignUserCert(pubKey, req.Principal, 8)
			if err != nil {
				resp["cert_error"] = err.Error()
			} else {
				resp["certificate"] = certPEM
				resp["cert_path"] = fmt.Sprintf("~/.hpc101/%s-key-cert.pub", req.Principal)
			}
		} else {
			resp["cert_warning"] = "no registered key; run 'hpc101 register-key <pubkey>' first"
		}
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
	s, ok := h.store.(*serializedStore)
	if !ok {
		http.Error(w, `{"error":"store does not support release"}`, http.StatusInternalServerError)
		return
	}
	if err := s.ReleaseLeaseIf(principal, lease.TriggerManual, func(l *Lease) bool { return true }, h.runtime, h.drainer); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": string(lease.StateReclaimed)})
}

func (h *Handler) handleProblems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	// Return unique problem IDs from submissions.
	seen := map[string]bool{}
	var problems []map[string]string
	for _, rec := range h.submissions {
		if !seen[rec.ProblemID] {
			seen[rec.ProblemID] = true
			problems = append(problems, map[string]string{
				"id":    rec.ProblemID,
				"title": rec.ProblemID,
			})
		}
	}
	if len(problems) == 0 {
		problems = []map[string]string{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"problems": problems})
}

func (h *Handler) handleScores(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	// Return scores from completed submissions.
	type scoreEntry struct {
		ProblemID   string  `json:"problem_id"`
		Score       float64 `json:"score"`
		Performance float64 `json:"performance"`
		Status      string  `json:"status"`
	}
	var scores []scoreEntry
	for _, rec := range h.submissions {
		if rec.Result.Status == "Success" || rec.Result.Status == "Failed" {
			scores = append(scores, scoreEntry{
				ProblemID:   rec.ProblemID,
				Score:       rec.Result.Score,
				Performance: rec.Result.Performance,
				Status:      rec.Result.Status,
			})
		}
	}
	if len(scores) == 0 {
		scores = []scoreEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"scores": scores})
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
	if strings.TrimSpace(req.ProblemID) == "" || len(req.Files) == 0 {
		http.Error(w, `{"error":"problem_id and files are required"}`, http.StatusBadRequest)
		return
	}
	files := make(map[string][]byte)
	for name, b64 := range req.Files {
		if strings.TrimSpace(name) == "" {
			http.Error(w, `{"error":"file name cannot be empty"}`, http.StatusBadRequest)
			return
		}
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"bad base64 for %s"}`, name), http.StatusBadRequest)
			return
		}
		files[name] = data
	}
	id, err := h.submission.Submit(r.Context(), req.ProblemID, files)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// Track the submission for later result retrieval.
	principal := r.URL.Query().Get("principal")
	if principal == "" {
		principal = "anonymous"
	}
	h.submissions[id] = &SubmissionRecord{
		ID:        id,
		ProblemID: req.ProblemID,
		Principal: principal,
		Submitted: time.Now().UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"submission_id": id, "status": "submitted"})
}

// handleSubmissionByID handles GET /api/v1/submissions/{id} — query submission status and result.
func (h *Handler) handleSubmissionByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/submissions/")
	if id == "" {
		http.Error(w, `{"error":"missing submission id"}`, http.StatusBadRequest)
		return
	}

	// Return data from our tracking store (fast path) or query adapter.
	rec, ok := h.submissions[id]
	if ok && rec.Result.Status != "" && rec.Result.Status != "Queued" && rec.Result.Status != "Running" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec.Result)
		return
	}

	// Query the adapter for current status.
	if h.submission != nil {
		result, err := h.submission.QueryResult(r.Context(), id)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		if ok {
			h.submissions[id].Result = *result
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"submission_id": id, "status": "Queued"})
}

// handleKeys handles POST /api/v1/keys — register a student SSH public key.
func (h *Handler) handleKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.registerKey(w, r)
	case http.MethodGet:
		h.getKey(w, r)
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (h *Handler) registerKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Principal string `json:"principal"`
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if !principalPattern.MatchString(req.Principal) {
		http.Error(w, `{"error":"invalid principal"}`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.PublicKey) == "" {
		http.Error(w, `{"error":"public_key is required"}`, http.StatusBadRequest)
		return
	}
	h.keys[req.Principal] = strings.TrimSpace(req.PublicKey)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "registered", "principal": req.Principal})
}

func (h *Handler) getKey(w http.ResponseWriter, r *http.Request) {
	principal := r.URL.Query().Get("principal")
	if !principalPattern.MatchString(principal) {
		http.Error(w, `{"error":"invalid principal"}`, http.StatusBadRequest)
		return
	}
	key, ok := h.keys[principal]
	if !ok {
		http.Error(w, `{"error":"no registered key"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"principal": principal, "public_key": key})
}

// handleSSHInfo handles GET /api/v1/ssh-info — return bastion ProxyJump config.
func (h *Handler) handleSSHInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	principal := r.URL.Query().Get("principal")
	if !principalPattern.MatchString(principal) {
		http.Error(w, `{"error":"invalid principal"}`, http.StatusBadRequest)
		return
	}
	l, err := h.store.LookupByPrincipal(principal)
	if err != nil || l == nil || l.State != lease.StateActive {
		http.Error(w, `{"error":"no active lease"}`, http.StatusNotFound)
		return
	}
	resp := map[string]interface{}{
		"bastion_host":    "bastion.hpc101-platform.svc.cluster.local",
		"bastion_port":    2222,
		"bastion_user":    "bastion",
		"container_host":  l.Host,
		"container_port":  l.Port,
		"container_user":  "student",
		"principal":       principal,
		"ssh_config":      fmt.Sprintf("Host hpc101-bastion\n  HostName bastion.hpc101-platform.svc.cluster.local\n  Port 2222\n  User bastion\n  IdentityFile ~/.hpc101/%s-key\n  CertificateFile ~/.hpc101/%s-key-cert.pub\n  IdentitiesOnly yes\n  ForwardAgent no\n\nHost hpc101-container\n  HostName %s\n  Port %d\n  User student\n  ProxyJump hpc101-bastion\n", principal, principal, l.Host, l.Port),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
