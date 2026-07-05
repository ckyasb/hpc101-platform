// Package controller provides the hpc101-platform controller HTTP API.
// Currently implements the lease lookup endpoint consumed by the
// bastion's AuthorizedPrincipalsCommand.
package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
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

// ErrLeaseConflict is returned when a principal already has a non-terminal lease.
type ErrLeaseConflict struct {
	Principal string
	State     lease.ReleaseState
}

func (e *ErrLeaseConflict) Error() string {
	return fmt.Sprintf("principal %s already has a %s lease", e.Principal, e.State)
}

// LeaseCreator is the store-owned create path that serializes up/release
// per principal. It holds the store lock while checking existing state,
// reserving the principal, calling the runtime to create the container,
// and storing the resulting lease. If the runtime fails, the reservation
// is rolled back. If the store fails after runtime creation, cleanup is
// called to stop the container and the reservation is rolled back.
type LeaseCreator interface {
	CreateLeaseForPrincipal(principal string, create func() (*ServiceResult, error), buildLease func(*ServiceResult) *Lease, cleanup func(*ServiceResult) error) (*Lease, *ServiceResult, error)
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

// ProblemSyncService is the interface for projecting platform problems
// into CSOJ contest/problem records and returning the scoped CSOJ problem ID.
type ProblemSyncService interface {
	SyncProblem(ctx context.Context, course, contest, problemID, title, startTime, endTime string, cluster string, cpu, memory int, upload map[string]interface{}, workflow []map[string]interface{}, score map[string]interface{}) (csojID string, err error)
}

// SubmissionService is the interface for submitting solutions for judging
// and querying results. The adapter implements both submission and result retrieval.
type SubmissionService interface {
	Submit(ctx context.Context, problemID string, files map[string][]byte) (string, error)
	QueryResult(ctx context.Context, submissionID string) (*SubmissionResult, error)
}

// SubmissionResult holds the judging outcome from CSOJ.
type SubmissionResult struct {
	SubmissionID string          `json:"submission_id"`
	ProblemID    string          `json:"problem_id"`
	Status       string          `json:"status"` // Queued, Running, Success, Failed
	Score        float64         `json:"score"`
	Performance  float64         `json:"performance"`
	Info         string          `json:"info,omitempty"`
	Containers   []ContainerInfo `json:"containers,omitempty"`
}

// ContainerInfo holds CSOJ container metadata for log streaming.
type ContainerInfo struct {
	ID    string `json:"id"`
	Image string `json:"image"`
}

// SubmissionRecord tracks a submission through its lifecycle.
type SubmissionRecord struct {
	ID             string           `json:"id"`
	ProblemID      string           `json:"problem_id"`
	Principal      string           `json:"principal"`
	Submitted      string           `json:"submitted_at"`
	Result         SubmissionResult `json:"result,omitempty"`
	IdempotencyKey string           `json:"idempotency_key,omitempty"`
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

// KeyStore persists registered SSH public keys. Implementations must be
// thread-safe and survive controller restart.
type KeyStore interface {
	RegisterKey(principal, publicKey string) error
	GetKey(principal string) (string, error)
}

// idempotencyEntry tracks an in-flight or completed submission for dedup.
type idempotencyEntry struct {
	payloadHash  string
	submissionID string // empty while in-flight
	done         chan struct{}
	err          error
}

// Handler serves the controller HTTP API.
type Handler struct {
	mu          sync.Mutex
	drainer     BastionDrainer
	store       LeaseStore
	runtime     ContainerCreator
	submission  SubmissionService
	certSigner  CertSigner
	keyStore    KeyStore
	problemSync ProblemSyncService
	submissions map[string]*SubmissionRecord // submissionID → record
	idempotency map[string]*idempotencyEntry // scopeKey → entry
	mux         *http.ServeMux
}

// HandlerOpts carries optional services for the controller handler.
// All fields are optional; nil means the feature is disabled.
type HandlerOpts struct {
	Drainer     BastionDrainer
	CertSigner  CertSigner
	ProblemSync ProblemSyncService
}

// NewHandlerWithOpts creates a handler with the given optional services.
// This is the single production constructor.
func NewHandlerWithOpts(store LeaseStore, runtime ContainerCreator, submission SubmissionService, opts HandlerOpts) *Handler {
	return newHandler(store, runtime, submission, opts.Drainer, opts.CertSigner, opts.ProblemSync)
}

// NewHandler creates a controller API handler with no optional services.
func NewHandler(store LeaseStore, runtime ContainerCreator, submission SubmissionService) *Handler {
	return NewHandlerWithOpts(store, runtime, submission, HandlerOpts{})
}

// NewHandlerWithDrainer creates a handler with a bastion drainer.
func NewHandlerWithDrainer(store LeaseStore, runtime ContainerCreator, submission SubmissionService, drainer BastionDrainer) *Handler {
	return NewHandlerWithOpts(store, runtime, submission, HandlerOpts{Drainer: drainer})
}

// NewHandlerWithDrainerAndSigner creates a handler with both drainer and cert signer.
func NewHandlerWithDrainerAndSigner(store LeaseStore, runtime ContainerCreator, submission SubmissionService, drainer BastionDrainer, signer CertSigner) *Handler {
	return NewHandlerWithOpts(store, runtime, submission, HandlerOpts{Drainer: drainer, CertSigner: signer})
}

func newHandler(store LeaseStore, runtime ContainerCreator, submission SubmissionService, drainer BastionDrainer, signer CertSigner, sync ProblemSyncService) *Handler {
	var ks KeyStore
	if kstore, ok := store.(KeyStore); ok {
		ks = kstore
	} else {
		ks = &inMemKeyStore{keys: make(map[string]string)}
	}
	h := &Handler{
		store:       store,
		runtime:     runtime,
		submission:  submission,
		drainer:     drainer,
		certSigner:  signer,
		keyStore:    ks,
		problemSync: sync,
		submissions: make(map[string]*SubmissionRecord),
		idempotency: make(map[string]*idempotencyEntry),
		mux:         http.NewServeMux(),
	}
	h.mux.HandleFunc("/api/v1/leases", h.handleLeases)
	h.mux.HandleFunc("/api/v1/services", h.handleCreateService)
	h.mux.HandleFunc("/api/v1/release", h.handleRelease)
	h.mux.HandleFunc("/api/v1/problems", h.handleProblems)
	h.mux.HandleFunc("/api/v1/problems/sync", h.handleProblemSync)
	h.mux.HandleFunc("/api/v1/scores", h.handleScores)
	h.mux.HandleFunc("/api/v1/submissions", h.handleSubmissions)
	h.mux.HandleFunc("/api/v1/submissions/", h.handleSubmissionByID)
	h.mux.HandleFunc("/api/v1/keys", h.handleKeys)
	h.mux.HandleFunc("/api/v1/ssh-info", h.handleSSHInfo)
	h.mux.HandleFunc("/api/v1/bastion/roster", h.handleBastionRoster)
	h.mux.HandleFunc("/api/v1/submissions/logs/", h.handleSubmissionLogs)
	h.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	return h
}

// inMemKeyStore is a simple in-memory key store with mutex protection.
type inMemKeyStore struct {
	mu   sync.Mutex
	keys map[string]string
}

func (s *inMemKeyStore) RegisterKey(principal, publicKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[principal] = publicKey
	return nil
}

func (s *inMemKeyStore) GetKey(principal string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[principal]
	if !ok {
		return "", fmt.Errorf("no registered key for %s", principal)
	}
	return k, nil
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

	// Use the store-owned atomic create path to prevent TOCTOU races.
	// The store holds its lock while checking existing state, reserving the
	// principal, calling the runtime, and storing the resulting lease.
	creator, ok := h.store.(LeaseCreator)
	if !ok {
		http.Error(w, `{"error":"store does not support atomic lease creation"}`, http.StatusInternalServerError)
		return
	}

	// Store-owned atomic create path with cleanup callback.
	rt := h.runtime
	_, result, err := creator.CreateLeaseForPrincipal(req.Principal,
		func() (*ServiceResult, error) {
			return rt.CreateService(req)
		},
		func(res *ServiceResult) *Lease {
			return lease.NewLease(req.Principal, res.ContainerID,
				"svc-"+req.Principal, res.Host, res.Port, 8*time.Hour, 30*time.Minute)
		},
		func(res *ServiceResult) error {
			return rt.StopService(res.ContainerID)
		},
	)
	if err != nil {
		// Use typed error for conflict detection.
		var conflict *ErrLeaseConflict
		if errors.As(err, &conflict) {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusConflict)
		} else {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		}
		return
	}

	// Build response.
	l := lease.NewLease(req.Principal, result.ContainerID,
		"svc-"+req.Principal, result.Host, result.Port, 8*time.Hour, 30*time.Minute)
	h.writeServiceResponse(w, req, result, l)
}

// writeServiceResponse writes the standard create-service JSON response,
// including certificate signing if configured.
func (h *Handler) writeServiceResponse(w http.ResponseWriter, req CreateServiceRequest, result *ServiceResult, l *Lease) {
	resp := map[string]interface{}{
		"container_id": result.ContainerID,
		"host":         result.Host,
		"port":         result.Port,
		"state":        string(l.State),
	}

	// Sign a short-lived SSH certificate if we have a CA signer and the
	// student has registered a public key.
	if h.certSigner != nil {
		pubKey, err := h.keyStore.GetKey(req.Principal)
		if err == nil {
			certPEM, certErr := h.certSigner.SignUserCert(pubKey, req.Principal, 8)
			if certErr != nil {
				resp["cert_error"] = certErr.Error()
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

// computeScopeKey generates a deterministic key from principal,
// mapped CSOJ problem ID, and sorted file names (without content).
// Used to detect duplicate/incompatible submits for the same scope.
func computeScopeKey(principal, problemID string, files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[i] > names[j] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|", principal, problemID)
	for _, name := range names {
		fmt.Fprintf(h, "%s|", name)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// computePayloadHash generates a SHA-256 from sorted file names + content.
// Used to detect exact vs incompatible duplicate submits.
func computePayloadHash(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[i] > names[j] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	h := sha256.New()
	for _, name := range names {
		fmt.Fprintf(h, "%s:", name)
		h.Write(files[name])
		fmt.Fprintf(h, "|")
	}
	return hex.EncodeToString(h.Sum(nil))
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
	s, ok := h.store.(ReleaseOps)
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

func (h *Handler) handleProblemSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if h.problemSync == nil {
		http.Error(w, `{"error":"problem sync not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Course    string                   `json:"course"`
		Contest   string                   `json:"contest"`
		ProblemID string                   `json:"problem_id"`
		Title     string                   `json:"title"`
		StartTime string                   `json:"start_time"`
		EndTime   string                   `json:"end_time"`
		Cluster   string                   `json:"cluster"`
		CPU       int                      `json:"cpu"`
		Memory    int                      `json:"memory"`
		Upload    map[string]interface{}   `json:"upload"`
		Workflow  []map[string]interface{} `json:"workflow"`
		Score     map[string]interface{}   `json:"score"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if req.Course == "" || req.Contest == "" || req.ProblemID == "" {
		http.Error(w, `{"error":"course, contest, and problem_id are required"}`, http.StatusBadRequest)
		return
	}

	// Require a mapping-capable store before calling the adapter.
	mapper, hasMapper := h.store.(interface {
		MapProblem(string, string, string, string) error
	})
	if !hasMapper {
		http.Error(w, `{"error":"store does not support problem mapping"}`, http.StatusInternalServerError)
		return
	}

	csojID, err := h.problemSync.SyncProblem(r.Context(),
		req.Course, req.Contest, req.ProblemID, req.Title,
		req.StartTime, req.EndTime,
		req.Cluster, req.CPU, req.Memory,
		req.Upload, req.Workflow, req.Score)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// Persist the mapping; fail if save fails.
	if err := mapper.MapProblem(req.Course, req.Contest, req.ProblemID, csojID); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"save mapping: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"status":              "synced",
		"course":              req.Course,
		"contest":             req.Contest,
		"platform_problem_id": req.ProblemID,
		"csoj_problem_id":     csojID,
	})
}

func (h *Handler) handleProblems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	// Collect submissions from handler memory and store.
	recs := make(map[string]*SubmissionRecord)
	h.mu.Lock()
	for k, v := range h.submissions {
		recs[k] = v
	}
	h.mu.Unlock()
	if s, ok := h.store.(interface{ AllSubmissions() []*SubmissionRecord }); ok {
		for _, r := range s.AllSubmissions() {
			if _, exists := recs[r.ID]; !exists {
				recs[r.ID] = r
			}
		}
	}
	seen := map[string]bool{}
	var problems []map[string]string
	for _, rec := range recs {
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
	recs := make(map[string]*SubmissionRecord)
	h.mu.Lock()
	for k, v := range h.submissions {
		recs[k] = v
	}
	h.mu.Unlock()
	if s, ok := h.store.(interface{ AllSubmissions() []*SubmissionRecord }); ok {
		for _, r := range s.AllSubmissions() {
			if _, exists := recs[r.ID]; !exists {
				recs[r.ID] = r
			}
		}
	}
	type scoreEntry struct {
		ProblemID   string  `json:"problem_id"`
		Score       float64 `json:"score"`
		Performance float64 `json:"performance"`
		Status      string  `json:"status"`
	}
	var scores []scoreEntry
	for _, rec := range recs {
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
	// Track the submission for later result retrieval.
	principal := r.URL.Query().Get("principal")
	course := r.URL.Query().Get("course")
	contest := r.URL.Query().Get("contest")

	// Require course and contest for problem resolution. Reject unmapped problems.
	if course == "" || contest == "" {
		http.Error(w, `{"error":"course and contest query parameters are required for submissions"}`, http.StatusBadRequest)
		return
	}
	resolver, hasResolver := h.store.(interface {
		ResolveProblem(string, string, string) string
	})
	if !hasResolver {
		http.Error(w, `{"error":"store does not support problem mapping"}`, http.StatusInternalServerError)
		return
	}
	mappedID := resolver.ResolveProblem(course, contest, req.ProblemID)
	if mappedID == "" {
		http.Error(w, `{"error":"problem not mapped for this course/contest; sync problem first"}`, http.StatusNotFound)
		return
	}

	// Idempotency: split into scope key (principal + problem + file names)
	// and payload hash (file contents). This allows exact duplicate detection
	// and incompatible duplicate rejection.
	scopeKey := computeScopeKey(principal, mappedID, files)
	payloadHash := computePayloadHash(files)

	h.mu.Lock()
	existing, scopeExists := h.idempotency[scopeKey]
	h.mu.Unlock()

	if scopeExists {
		if existing.submissionID != "" {
			// Completed entry: check if exact duplicate or incompatible.
			h.mu.Lock()
			rec, _ := h.submissions[existing.submissionID]
			h.mu.Unlock()
			if rec != nil && rec.IdempotencyKey == payloadHash {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]string{
					"submission_id": rec.ID,
					"status":        "duplicate",
				})
				return
			}
			// Incompatible: same scope, different payload → 409.
			http.Error(w, `{"error":"incompatible duplicate submission for this principal/problem; content differs from existing"}`, http.StatusConflict)
			return
		}
		// In-flight entry: wait for completion.
		if existing.payloadHash == payloadHash {
			// Same payload — wait for the in-flight submit to complete.
			select {
			case <-existing.done:
				if existing.err != nil {
					// Original submit failed; clear and allow retry.
					h.mu.Lock()
					delete(h.idempotency, scopeKey)
					h.mu.Unlock()
					http.Error(w, fmt.Sprintf(`{"error":"previous in-flight submit failed: %s"}`, existing.err.Error()), http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]string{
					"submission_id": existing.submissionID,
					"status":        "duplicate",
				})
				return
			case <-r.Context().Done():
				http.Error(w, `{"error":"request cancelled while waiting for in-flight submit"}`, http.StatusServiceUnavailable)
				return
			}
		}
		// Different payload while in-flight → 409.
		http.Error(w, `{"error":"incompatible duplicate submission for this principal/problem; content differs from in-flight"}`, http.StatusConflict)
		return
	}

	// Reserve the idempotency scope under lock before calling CSOJ.
	entry := &idempotencyEntry{
		payloadHash: payloadHash,
		done:        make(chan struct{}),
	}
	h.mu.Lock()
	// Double-check after acquiring lock.
	if existing2, ok := h.idempotency[scopeKey]; ok {
		if existing2.submissionID != "" {
			rec2, _ := h.submissions[existing2.submissionID]
			if rec2 != nil && rec2.IdempotencyKey == payloadHash {
				h.mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]string{"submission_id": rec2.ID, "status": "duplicate"})
				return
			}
		}
		h.mu.Unlock()
		http.Error(w, `{"error":"incompatible duplicate submission for this principal/problem"}`, http.StatusConflict)
		return
	}
	h.idempotency[scopeKey] = entry
	h.mu.Unlock()

	// Call CSOJ Submit WITHOUT holding the store lock.
	id, err := h.submission.Submit(r.Context(), mappedID, files)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		// Clear the reservation so a retry can proceed.
		entry.err = err
		close(entry.done)
		delete(h.idempotency, scopeKey)
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// Save submission record.
	if principal == "" {
		principal = "anonymous"
	}
	rec := &SubmissionRecord{
		ID:             id,
		ProblemID:      req.ProblemID,
		Principal:      principal,
		Submitted:      time.Now().UTC().Format(time.RFC3339),
		IdempotencyKey: payloadHash,
	}
	h.submissions[id] = rec
	entry.submissionID = id
	close(entry.done)
	// Persist in store if available.
	if s, ok := h.store.(interface{ SaveSubmission(*SubmissionRecord) error }); ok {
		if saveErr := s.SaveSubmission(rec); saveErr != nil {
			http.Error(w, fmt.Sprintf(`{"error":"save submission: %s"}`, saveErr.Error()), http.StatusInternalServerError)
			return
		}
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

	// Load record from memory or store.
	h.mu.Lock()
	rec, ok := h.submissions[id]
	h.mu.Unlock()
	if !ok {
		if s, ok2 := h.store.(interface {
			GetSubmission(string) (*SubmissionRecord, error)
		}); ok2 {
			r, err := s.GetSubmission(id)
			if err == nil {
				rec = r
				ok = true
			}
		}
	}
	if !ok {
		http.Error(w, `{"error":"submission not found"}`, http.StatusNotFound)
		return
	}

	// Return cached terminal result without re-querying.
	if rec.Result.Status != "" && rec.Result.Status != "Queued" && rec.Result.Status != "Running" {
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
		// Update both memory and store.
		rec.Result = *result
		h.mu.Lock()
		h.submissions[id] = rec
		h.mu.Unlock()
		if s, ok2 := h.store.(interface{ SaveSubmission(*SubmissionRecord) error }); ok2 {
			_ = s.SaveSubmission(rec)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"submission_id": id, "status": "Queued"})
}

// handleSubmissionLogs handles GET /api/v1/submissions/logs/{id} — stream container logs.
func (h *Handler) handleSubmissionLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/submissions/logs/")
	if id == "" {
		http.Error(w, `{"error":"missing submission id"}`, http.StatusBadRequest)
		return
	}
	// Look up submission to find container IDs for log streaming.
	var rec *SubmissionRecord
	h.mu.Lock()
	rec = h.submissions[id]
	h.mu.Unlock()
	if rec == nil {
		// Try store
		if s, ok := h.store.(interface {
			GetSubmission(string) (*SubmissionRecord, error)
		}); ok {
			r, err := s.GetSubmission(id)
			if err == nil {
				rec = r
			}
		}
	}
	if rec == nil {
		http.Error(w, `{"error":"submission not found"}`, http.StatusNotFound)
		return
	}

	// Query the adapter for the latest result if we don't have containers yet.
	if len(rec.Result.Containers) == 0 && h.submission != nil {
		queryID := rec.Result.SubmissionID
		if queryID == "" {
			queryID = rec.ID
		}
		result, err := h.submission.QueryResult(r.Context(), queryID)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"query result: %s"}`, err.Error()), http.StatusBadGateway)
			return
		}
		rec.Result = *result
		if rec.Result.SubmissionID == "" {
			rec.Result.SubmissionID = queryID
		}
		h.mu.Lock()
		h.submissions[id] = rec
		h.mu.Unlock()
		if s, ok2 := h.store.(interface{ SaveSubmission(*SubmissionRecord) error }); ok2 {
			_ = s.SaveSubmission(rec)
		}
	}
	if len(rec.Result.Containers) == 0 {
		http.Error(w, `{"error":"submission has no containers yet; retry later"}`, http.StatusConflict)
		return
	}

	// Stream logs via the adapter if it supports it.
	type logStreamer interface {
		StreamLogs(ctx context.Context, submissionID, containerID string, cb func(stream, data string) error) error
	}
	if streamer, ok := interface{}(h.submission).(logStreamer); ok {
		containerID := rec.Result.Containers[0].ID
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		err := streamer.StreamLogs(r.Context(), rec.Result.SubmissionID, containerID,
			func(stream, data string) error {
				fmt.Fprintf(w, "[%s] %s\n", stream, data)
				if flusher != nil {
					flusher.Flush()
				}
				return nil
			})
		if err != nil {
			fmt.Fprintf(w, "\nerror: %v\n", err)
		}
		return
	}
	http.Error(w, `{"error":"log streaming not available"}`, http.StatusServiceUnavailable)
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
	if err := h.keyStore.RegisterKey(req.Principal, strings.TrimSpace(req.PublicKey)); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "registered", "principal": req.Principal, "config_dir": "~/.hpc101"})
}

func (h *Handler) getKey(w http.ResponseWriter, r *http.Request) {
	principal := r.URL.Query().Get("principal")
	if !principalPattern.MatchString(principal) {
		http.Error(w, `{"error":"invalid principal"}`, http.StatusBadRequest)
		return
	}
	key, err := h.keyStore.GetKey(principal)
	if err != nil {
		http.Error(w, `{"error":"no registered key"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"principal": principal, "public_key": key})
}

// handleSSHInfo handles GET /api/v1/ssh-info — return bastion ProxyJump config.
//
// The bastion host/port default to the internal k8s DNS. Set
// HPC101_BASTION_PUBLIC_HOST / HPC101_BASTION_PUBLIC_PORT to
// override with a hostname reachable from student laptops (e.g.
// the SSH gateway or a NodePort/LoadBalancer address).
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

	bastionHost := "bastion.hpc101-platform.svc.cluster.local"
	if v := os.Getenv("HPC101_BASTION_PUBLIC_HOST"); v != "" {
		bastionHost = v
	}
	bastionPort := "2222"
	if v := os.Getenv("HPC101_BASTION_PUBLIC_PORT"); v != "" {
		bastionPort = v
	}

	resp := map[string]interface{}{
		"bastion_host":   bastionHost,
		"bastion_port":   bastionPort,
		"bastion_user":   "bastion",
		"container_host": l.Host,
		"container_port": l.Port,
		"container_user": "student",
		"principal":      principal,
		"config_dir":     "~/.hpc101",
	}
	// If using a public gateway (not internal DNS), the bastion User
	// encodes the routing info that the SSH gateway needs: <principal>+bastion.
	bastionUser := "bastion"
	if bastionHost != "bastion.hpc101-platform.svc.cluster.local" {
		bastionUser = principal + "+bastion"
	}

	sshCfg := fmt.Sprintf(
		"Host hpc101-bastion\n  HostName %s\n  Port %s\n  User %s\n"+
			"  IdentityFile ~/.hpc101/%s-key\n  CertificateFile ~/.hpc101/%s-key-cert.pub\n"+
			"  IdentitiesOnly yes\n  ForwardAgent no\n\n"+
			"Host hpc101-container\n  HostName %%s\n  Port %%d\n  User student\n  ProxyJump hpc101-bastion\n",
		bastionHost, bastionPort, bastionUser, principal, principal)
	resp["ssh_config"] = fmt.Sprintf(sshCfg, l.Host, l.Port)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
