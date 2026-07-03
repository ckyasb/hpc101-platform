package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"hpc101-platform/lease"
)

type memStore map[string]*lease.Lease

func (m memStore) LookupByPrincipal(p string) (*Lease, error) {
	return m[p], nil
}
func (m memStore) UpsertLease(l *Lease) error {
	m[l.Owner] = l
	return nil
}

type fakeRuntime struct {
	lastPrincipal      string
	lastImage          string
	lastSSHKey         string
	lastCourse         string
	lastProblem        string
	stoppedContainerID string
}

func (f *fakeRuntime) CreateService(req CreateServiceRequest) (*ServiceResult, error) {
	f.lastPrincipal = req.Principal
	f.lastImage = req.Image
	f.lastSSHKey = req.SSHKey
	f.lastCourse = req.Course
	f.lastProblem = req.Problem
	return &ServiceResult{ContainerID: "ctr-" + req.Principal, Host: "10.0.0.5", Port: 2222}, nil
}
func (f *fakeRuntime) StopService(containerID string) error {
	f.stoppedContainerID = containerID
	return nil
}

func TestHandleLeasesActive(t *testing.T) {
	l := lease.NewLease("student-42", "abc", "svc-student-42", "10.0.0.5", 2222, 8*time.Hour, 30*time.Minute)
	store := NewSerializedStore()
	store.UpsertLease(l)
	h := NewHandler(store, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/leases?principal=student-42", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp["container_host"] != "10.0.0.5" {
		t.Errorf("host: %v", resp["container_host"])
	}
	if resp["container_port"] != float64(2222) {
		t.Errorf("port: %v", resp["container_port"])
	}
}

func TestHandleLeasesRejectsInjection(t *testing.T) {
	h := NewHandler(memStore{}, nil, nil)

	for _, p := range []string{
		"student;rm",
		"$(whoami)",
		"`id`",
		"student\nbad",
		"",
	} {
		target := "/api/v1/leases?principal=" + url.QueryEscape(p)
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("principal %q: expected 400, got %d", p, rec.Code)
		}
	}
}

func TestCreateServiceWritesActiveLease(t *testing.T) {
	rt := &fakeRuntime{}
	store := memStore{}
	h := NewHandler(store, rt, nil)

	body := `{"principal":"student-42","image":"hpc101-platform/container:latest","ssh_key":"ssh-rsa AAA...","course":"cs101","problem":"hw1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/services", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if rt.lastPrincipal != "student-42" {
		t.Errorf("principal: got %q", rt.lastPrincipal)
	}
	if rt.lastCourse != "cs101" || rt.lastProblem != "hw1" {
		t.Errorf("course/problem: %s/%s", rt.lastCourse, rt.lastProblem)
	}
	if rt.lastSSHKey != "ssh-rsa AAA..." {
		t.Errorf("ssh_key: got %q", rt.lastSSHKey)
	}

	// Verify lease was written and is retrievable
	l, _ := store.LookupByPrincipal("student-42")
	if l == nil {
		t.Fatal("no lease written for student-42")
	}
	if l.Port != 2222 {
		t.Errorf("lease port: got %d", l.Port)
	}

	// Verify GET /api/v1/leases returns the new lease
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/leases?principal=student-42", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("lease GET returned %d", rec2.Code)
	}
}

func TestCreateServiceRejectsEmptyFields(t *testing.T) {
	rt := &fakeRuntime{}
	h := NewHandler(memStore{}, rt, nil)

	for _, body := range []string{
		`{"principal":"s1","image":"","ssh_key":"k","course":"c","problem":"p"}`,
		`{"principal":"s1","image":"i","ssh_key":"","course":"c","problem":"p"}`,
		`{"principal":"s1","image":"i","ssh_key":"k","course":"","problem":"p"}`,
		`{"principal":"invalid!","image":"i","ssh_key":"k","course":"c","problem":"p"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/services", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: expected 400, got %d", body, rec.Code)
		}
	}
}

func TestHandleLeasesNoActiveLease(t *testing.T) {
	h := NewHandler(memStore{}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/leases?principal=nobody", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for no lease, got %d", rec.Code)
	}
}

type fakeSubmission struct {
	lastProblemID string
	lastFiles     map[string][]byte
	lastQueryID   string
	err           error
	id            string
	result        *SubmissionResult
}

func (f *fakeSubmission) Submit(ctx context.Context, problemID string, files map[string][]byte) (string, error) {
	f.lastProblemID = problemID
	f.lastFiles = files
	if f.err != nil {
		return "", f.err
	}
	if f.id == "" {
		return "sub-123", nil
	}
	return f.id, nil
}

func (f *fakeSubmission) QueryResult(ctx context.Context, submissionID string) (*SubmissionResult, error) {
	f.lastQueryID = submissionID
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &SubmissionResult{SubmissionID: submissionID, Status: "Queued"}, nil
}

func TestSubmitHandlerSuccess(t *testing.T) {
	f := &fakeSubmission{}
	h := NewHandler(memStore{}, nil, f)
	body := `{"problem_id":"p1","files":{"main.c":"aW50IG1haW4oKXt9"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if f.lastProblemID != "p1" {
		t.Errorf("problem_id: %s", f.lastProblemID)
	}
	// Assert decoded file content
	if string(f.lastFiles["main.c"]) != "int main(){}" {
		t.Errorf("decoded file: %s", f.lastFiles["main.c"])
	}
	// Assert response body
	var resp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["submission_id"] != "sub-123" {
		t.Errorf("submission_id: %s", resp["submission_id"])
	}
	if resp["status"] != "submitted" {
		t.Errorf("status: %s", resp["status"])
	}
}

func TestSubmitHandlerMissingService(t *testing.T) {
	h := NewHandler(memStore{}, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", strings.NewReader(`{"problem_id":"p1","files":{"a":"b"}}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestSubmitHandlerEmptyInputs(t *testing.T) {
	h := NewHandler(memStore{}, nil, &fakeSubmission{})
	for _, body := range []string{`{"problem_id":"","files":{"a":"b"}}`, `{"problem_id":"p1","files":{}}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: expected 400, got %d", body, rec.Code)
		}
	}
}

func TestSubmitHandlerServiceError(t *testing.T) {
	f := &fakeSubmission{err: fmt.Errorf("CSOJ unavailable")}
	h := NewHandler(memStore{}, nil, f)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", strings.NewReader(`{"problem_id":"p1","files":{"a":"YQ=="}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSubmitHandlerMalformedJSON(t *testing.T) {
	h := NewHandler(memStore{}, nil, &fakeSubmission{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", strings.NewReader(`not json`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestSubmitHandlerBadBase64(t *testing.T) {
	h := NewHandler(memStore{}, nil, &fakeSubmission{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", strings.NewReader(`{"problem_id":"p1","files":{"a":"!!!"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad base64, got %d", rec.Code)
	}
}

func TestSubmitHandlerEmptyFileName(t *testing.T) {
	h := NewHandler(memStore{}, nil, &fakeSubmission{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", strings.NewReader(`{"problem_id":"p1","files":{"":"YQ=="}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty file name, got %d", rec.Code)
	}
}

func TestSubmitHandlerWhitespaceProblemID(t *testing.T) {
	h := NewHandler(memStore{}, nil, &fakeSubmission{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", strings.NewReader(`{"problem_id":"   ","files":{"a":"YQ=="}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for whitespace problem_id, got %d", rec.Code)
	}
}

func TestSubmitHandlerMethodRejection(t *testing.T) {
	h := NewHandler(memStore{}, nil, &fakeSubmission{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/submissions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestReleaseSuccess(t *testing.T) {
	l := lease.NewLease("student-42", "ctr-abc", "svc-student-42", "10.0.0.5", 2222, 8*time.Hour, 30*time.Minute)
	store := NewSerializedStore()
	store.UpsertLease(l)
	rt := &fakeRuntime{}
	h := NewHandler(store, rt, nil)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/release?principal=student-42", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	final, _ := store.LookupByPrincipal("student-42")
	if final == nil || final.State != lease.StateReclaimed {
		t.Errorf("state: %v", final)
	}
	if final != nil && final.ReleasedBy != lease.TriggerManual {
		t.Errorf("ReleasedBy: %s", final.ReleasedBy)
	}
	if rt.stoppedContainerID != "ctr-abc" {
		t.Errorf("stop not called, or wrong ID: %q", rt.stoppedContainerID)
	}
}

type fakeRuntimeFailing struct{}

func (f *fakeRuntimeFailing) CreateService(req CreateServiceRequest) (*ServiceResult, error) {
	return nil, fmt.Errorf("down")
}
func (f *fakeRuntimeFailing) StopService(containerID string) error {
	return fmt.Errorf("stop failed")
}

type blockingRuntime struct {
	enteredStop chan struct{}
	releaseStop chan struct{}
}

func (b *blockingRuntime) CreateService(req CreateServiceRequest) (*ServiceResult, error) {
	return nil, nil
}
func (b *blockingRuntime) StopService(containerID string) error {
	close(b.enteredStop)
	<-b.releaseStop
	return nil
}

func TestReleaseBlocksConcurrentUpsert(t *testing.T) {
	s := NewSerializedStore()
	l := lease.NewLease("student-42", "ctr-abc", "svc-student-42", "10.0.0.5", 2222, 8*time.Hour, 30*time.Minute)
	s.UpsertLease(l)

	br := &blockingRuntime{enteredStop: make(chan struct{}), releaseStop: make(chan struct{})}
	releaseDone := make(chan error, 1)
	go func() {
		releaseDone <- s.ReleaseLeaseIf("student-42", lease.TriggerManual, func(l *Lease) bool { return true }, br, nil)
	}()

	// Wait for release to enter StopService before attempting upsert
	select {
	case <-br.enteredStop:
	case <-time.After(2 * time.Second):
		t.Fatal("release never entered StopService")
	}

	// Now try to upsert — must block until release completes
	upsertDone := make(chan struct{})
	newLease := lease.NewLease("student-42", "ctr-def", "svc-student-42", "10.0.0.6", 2222, 8*time.Hour, 30*time.Minute)
	go func() {
		s.UpsertLease(newLease)
		close(upsertDone)
	}()

	// Upsert should NOT complete while release holds the lock
	select {
	case <-upsertDone:
		t.Fatal("upsert completed while release held the lock — serialization broken")
	case <-time.After(200 * time.Millisecond):
		// Expected: upsert is blocked
	}

	// Release stop → release finishes, then upsert should complete
	close(br.releaseStop)
	if err := <-releaseDone; err != nil {
		t.Errorf("release failed: %v", err)
	}
	select {
	case <-upsertDone:
	case <-time.After(2 * time.Second):
		t.Fatal("upsert still blocked after release completed")
	}

	// Final state: release wins (ctr-abc is reclaimed), upsert writes ctr-def after
	final, _ := s.LookupByPrincipal("student-42")
	if final == nil || final.ContainerID != "ctr-def" {
		t.Errorf("expected ctr-def after release+upsert, got %v", final)
	}
}

type orderedRecorder struct {
	events []string
}

func (o *orderedRecorder) Drain(p string) error {
	o.events = append(o.events, "drain:"+p)
	return nil
}
func (o *orderedRecorder) CreateService(req CreateServiceRequest) (*ServiceResult, error) {
	return nil, nil
}
func (o *orderedRecorder) StopService(cid string) error {
	o.events = append(o.events, "stop:"+cid)
	return nil
}

func TestReleaseCallsDrainerBeforeStop(t *testing.T) {
	s := NewSerializedStore()
	l := lease.NewLease("student-42", "ctr-abc", "svc-student-42", "10.0.0.5", 2222, 8*time.Hour, 30*time.Minute)
	s.UpsertLease(l)
	o := &orderedRecorder{}

	err := s.ReleaseLeaseIf("student-42", lease.TriggerManual, func(l *Lease) bool { return true }, o, o)
	if err != nil {
		t.Fatalf("ReleaseLeaseIf: %v", err)
	}
	expected := []string{"drain:student-42", "stop:ctr-abc"}
	if len(o.events) != len(expected) || o.events[0] != expected[0] || o.events[1] != expected[1] {
		t.Errorf("expected %v, got %v", expected, o.events)
	}
}

type failingDrainer struct{}

func (f failingDrainer) Drain(p string) error {
	return fmt.Errorf("drain failed")
}

func TestReleaseDrainFailureNoStop(t *testing.T) {
	s := NewSerializedStore()
	l := lease.NewLease("student-42", "ctr-abc", "svc-student-42", "10.0.0.5", 2222, 8*time.Hour, 30*time.Minute)
	s.UpsertLease(l)
	d := failingDrainer{}
	rt := &fakeRuntime{}

	err := s.ReleaseLeaseIf("student-42", lease.TriggerManual, func(l *Lease) bool { return true }, rt, d)
	if err == nil {
		t.Fatal("expected drain failure error")
	}
	if rt.stoppedContainerID != "" {
		t.Error("stop must not be called after drain failure")
	}
	// Lease should remain Active
	final, _ := s.LookupByPrincipal("student-42")
	if final == nil || final.State != lease.StateActive {
		t.Errorf("lease should be Active after drain failure, got %v", final)
	}
}

func TestReleaseStopFailureClearsMetadata(t *testing.T) {
	s := NewSerializedStore()
	l := lease.NewLease("student-42", "ctr-abc", "svc-student-42", "10.0.0.5", 2222, 8*time.Hour, 30*time.Minute)
	s.UpsertLease(l)
	rt := &fakeRuntimeFailing{}

	err := s.ReleaseLeaseIf("student-42", lease.TriggerManual, func(l *Lease) bool { return true }, rt, nil)
	if err == nil {
		t.Fatal("expected stop failure error")
	}
	final, _ := s.LookupByPrincipal("student-42")
	if final == nil || final.State != lease.StateActive {
		t.Errorf("lease should be Active after stop failure, got %v", final)
	}
	if final.ReleasedBy != "" {
		t.Errorf("ReleasedBy should be empty after stop failure, got %s", final.ReleasedBy)
	}
}

func TestMaxLifeTriggerRelease(t *testing.T) {
	s := NewSerializedStore()
	// Lease with 1ns max life => instantly expired
	l := lease.NewLease("student-42", "ctr-abc", "svc-student-42", "10.0.0.5", 2222, 1*time.Nanosecond, 30*time.Minute)
	time.Sleep(time.Millisecond)
	s.UpsertLease(l)
	rt := &fakeRuntime{}

	err := s.ReleaseLeaseIf("student-42", lease.TriggerMaxLife,
		func(l *Lease) bool { return l.IsExpired() },
		rt, nil)
	if err != nil {
		t.Fatalf("max-life release: %v", err)
	}
	final, _ := s.LookupByPrincipal("student-42")
	if final == nil || final.State != lease.StateReclaimed {
		t.Errorf("state: %v", final)
	}
	if final.ReleasedBy != lease.TriggerMaxLife {
		t.Errorf("ReleasedBy: %s", final.ReleasedBy)
	}
}

func TestIdleTriggerRelease(t *testing.T) {
	s := NewSerializedStore()
	l := lease.NewLease("student-42", "ctr-abc", "svc-student-42", "10.0.0.5", 2222, 8*time.Hour, 1*time.Nanosecond)
	time.Sleep(time.Millisecond)
	s.UpsertLease(l)
	rt := &fakeRuntime{}

	err := s.ReleaseLeaseIf("student-42", lease.TriggerIdle,
		func(l *Lease) bool { return l.IsIdle() },
		rt, nil)
	if err != nil {
		t.Fatalf("idle release: %v", err)
	}
	final, _ := s.LookupByPrincipal("student-42")
	if final == nil || final.State != lease.StateReclaimed {
		t.Errorf("state: %v", final)
	}
	if final.ReleasedBy != lease.TriggerIdle {
		t.Errorf("ReleasedBy: %s", final.ReleasedBy)
	}
}

func TestReleaseServiceDown(t *testing.T) {
	store := NewSerializedStore()
	l := lease.NewLease("student-42", "ctr-abc", "svc-student-42", "10.0.0.5", 2222, 8*time.Hour, 30*time.Minute)
	store.UpsertLease(l)
	h := NewHandler(store, &fakeRuntimeFailing{}, nil)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/release?principal=student-42", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
	final, _ := store.LookupByPrincipal("student-42")
	if final == nil || final.State != lease.StateActive {
		t.Errorf("lease should remain Active after failed stop, got %v", final)
	}
}


type fakeDiscovery struct {
	containers []DiscoveryContainer
	volumes    []DiscoveryVolume
	networks   []DiscoveryNetwork
}

func (f *fakeDiscovery) ListContainers(labels map[string]string) ([]DiscoveryContainer, error) {
	return f.containers, nil
}

func (f *fakeDiscovery) ListVolumes(labels map[string]string) ([]DiscoveryVolume, error) {
	return f.volumes, nil
}

func (f *fakeDiscovery) ListNetworks(labels map[string]string) ([]DiscoveryNetwork, error) {
	return f.networks, nil
}

func TestReattachLeases(t *testing.T) {
	s := NewSerializedStore()
	d := &fakeDiscovery{containers: []DiscoveryContainer{
		{ID: "ctr-1", Name: "svc-alice", Host: "10.0.0.5", Port: 2222,
			Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice"}},
		{ID: "ctr-2", Name: "orphan", Host: "10.0.0.6", Port: 2222,
			Labels: map[string]string{"platform.io/kind": "service"}}, // no owner
	}}

	result, err := ReattachLeases(s, d)
	if err != nil {
		t.Fatalf("ReattachLeases: %v", err)
	}
	if result.Reattached != 1 {
		t.Errorf("reattached: %d", result.Reattached)
	}
	if result.Orphaned != 1 {
		t.Errorf("orphaned: %d", result.Orphaned)
	}
	l, _ := s.LookupByPrincipal("alice")
	if l == nil || l.ContainerID != "ctr-1" {
		t.Errorf("lease not reattached: %v", l)
	}
}

func TestReattachRejectsNonSvcPrefix(t *testing.T) {
	s := NewSerializedStore()
	d := &fakeDiscovery{containers: []DiscoveryContainer{
		{ID: "ctr-1", Name: "csj-judge-1", Host: "10.0.0.5", Port: 2222,
			Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice"}},
	}}

	result, err := ReattachLeases(s, d)
	if err != nil {
		t.Fatalf("ReattachLeases: %v", err)
	}
	if result.Reattached != 0 {
		t.Errorf("csj- prefix should not be reattached: %d", result.Reattached)
	}
	if result.Orphaned != 1 {
		t.Errorf("expected 1 orphan, got %d", result.Orphaned)
	}
}

func TestReattachNilClient(t *testing.T) {
	s := NewSerializedStore()
	_, err := ReattachLeases(s, nil)
	if err == nil {
		t.Fatal("expected error for nil discovery client")
	}
}

func TestReattachReclaimsVolumes(t *testing.T) {
	s := NewSerializedStore()
	d := &fakeDiscovery{
		containers: []DiscoveryContainer{
			{ID: "c1", Name: "svc-alice", Host: "h", Port: 22, Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice"}},
		},
		volumes: []DiscoveryVolume{
			{Name: "svc-alice-vol", Driver: "local", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice"}},
			{Name: "svc-orphan-vol", Driver: "local", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "bob"}},
			{Name: "csj-judge-vol", Driver: "local", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "bob"}},
		},
	}
	result, err := ReattachLeases(s, d)
	if err != nil {
		t.Fatalf("ReattachLeases: %v", err)
	}
	if result.Reattached != 1 {
		t.Errorf("reattached: %d", result.Reattached)
	}
	// svc-alice-vol belongs to active owner alice, not orphan
	// svc-orphan-vol belongs to bob who has no active container -> orphan
	// csj-judge-vol has csj- prefix -> skipped
	if result.OrphanVolumes != 1 {
		t.Errorf("orphan volumes: expected 1, got %d", result.OrphanVolumes)
	}
}

func TestReattachPreservesCSJResources(t *testing.T) {
	s := NewSerializedStore()
	d := &fakeDiscovery{
		containers: []DiscoveryContainer{
			{ID: "c1", Name: "csj-judge-1", Host: "h", Port: 0, Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice"}},
		},
		volumes: []DiscoveryVolume{
			{Name: "csj-vol-1", Driver: "local", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice"}},
		},
		networks: []DiscoveryNetwork{
			{ID: "n1", Name: "csj-net-1", Driver: "bridge", Labels: map[string]string{"platform.io/kind": "service"}},
		},
	}
	result, err := ReattachLeases(s, d)
	if err != nil {
		t.Fatalf("ReattachLeases: %v", err)
	}
	// csj- prefixed container is orphan (no svc- prefix) but csj- volumes/networks are never reclaimed
	if result.Orphaned != 1 {
		t.Errorf("csj- container should be orphan: got %d", result.Orphaned)
	}
	if result.OrphanVolumes != 0 {
		t.Errorf("csj- volumes must not be counted as orphan: got %d", result.OrphanVolumes)
	}
	if result.OrphanNetworks != 0 {
		t.Errorf("csj- networks must not be counted as orphan: got %d", result.OrphanNetworks)
	}
}

func TestReattachNetworkDiscovery(t *testing.T) {
	s := NewSerializedStore()
	d := &fakeDiscovery{
		containers: []DiscoveryContainer{
			{ID: "c1", Name: "svc-alice", Host: "h", Port: 22, Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice"}},
		},
		networks: []DiscoveryNetwork{
			{ID: "n1", Name: "svc-alice-net", Driver: "bridge", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice"}},
			{ID: "n2", Name: "svc-orphan-net", Driver: "bridge", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "charlie"}},
		},
	}
	result, err := ReattachLeases(s, d)
	if err != nil {
		t.Fatalf("ReattachLeases: %v", err)
	}
	if result.Reattached != 1 {
		t.Errorf("reattached: %d", result.Reattached)
	}
	if result.OrphanNetworks != 1 {
		t.Errorf("orphan networks: expected 1, got %d", result.OrphanNetworks)
	}
}
