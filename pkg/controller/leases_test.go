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
	s := NewSerializedStore()
	// Pre-populate problem mapping so submit resolves correctly.
	if err := s.MapProblem("cs101", "c1", "p1", "cs101--p1"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s, nil, f)
	body := `{"problem_id":"p1","files":{"main.c":"aW50IG1haW4oKXt9"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions?course=cs101&contest=c1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if f.lastProblemID != "cs101--p1" {
		t.Errorf("problem_id: %s (expected mapped ID)", f.lastProblemID)
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
	s := NewSerializedStore()
	if err := s.MapProblem("cs101", "c1", "p1", "cs101--p1"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions?course=cs101&contest=c1", strings.NewReader(`{"problem_id":"p1","files":{"a":"b"}}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestSubmitHandlerEmptyInputs(t *testing.T) {
	s := NewSerializedStore()
	if err := s.MapProblem("cs101", "c1", "p1", "cs101--p1"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s, nil, &fakeSubmission{})
	for _, body := range []string{`{"problem_id":"","files":{"a":"b"}}`, `{"problem_id":"p1","files":{}}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions?course=cs101&contest=c1", strings.NewReader(body))
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
	s := NewSerializedStore()
	if err := s.MapProblem("cs101", "c1", "p1", "cs101--p1"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s, nil, f)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions?course=cs101&contest=c1", strings.NewReader(`{"problem_id":"p1","files":{"a":"YQ=="}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSubmitHandlerMalformedJSON(t *testing.T) {
	s := NewSerializedStore()
	if err := s.MapProblem("cs101", "c1", "p1", "cs101--p1"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s, nil, &fakeSubmission{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions?course=cs101&contest=c1", strings.NewReader(`not json`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestSubmitHandlerBadBase64(t *testing.T) {
	s := NewSerializedStore()
	if err := s.MapProblem("cs101", "c1", "p1", "cs101--p1"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s, nil, &fakeSubmission{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions?course=cs101&contest=c1", strings.NewReader(`{"problem_id":"p1","files":{"a":"!!!"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad base64, got %d", rec.Code)
	}
}

func TestSubmitHandlerEmptyFileName(t *testing.T) {
	s := NewSerializedStore()
	if err := s.MapProblem("cs101", "c1", "p1", "cs101--p1"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s, nil, &fakeSubmission{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions?course=cs101&contest=c1", strings.NewReader(`{"problem_id":"p1","files":{"":"YQ=="}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty file name, got %d", rec.Code)
	}
}

func TestSubmitHandlerWhitespaceProblemID(t *testing.T) {
	s := NewSerializedStore()
	if err := s.MapProblem("cs101", "c1", "p1", "cs101--p1"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s, nil, &fakeSubmission{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions?course=cs101&contest=c1", strings.NewReader(`{"problem_id":"   ","files":{"a":"YQ=="}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for whitespace problem_id, got %d", rec.Code)
	}
}

func TestSubmitHandlerMethodRejection(t *testing.T) {
	s := NewSerializedStore()
	if err := s.MapProblem("cs101", "c1", "p1", "cs101--p1"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s, nil, &fakeSubmission{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/submissions?course=cs101&contest=c1", nil)
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
	containers      []DiscoveryContainer
	volumes         []DiscoveryVolume
	networks        []DiscoveryNetwork
	removedVolumes  []string
	removedNetworks []string
	removeVolErr    error
	removeNetErr    error
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

func (f *fakeDiscovery) RemoveVolume(ctx context.Context, name string) error {
	if f.removeVolErr != nil {
		return f.removeVolErr
	}
	f.removedVolumes = append(f.removedVolumes, name)
	return nil
}

func (f *fakeDiscovery) RemoveNetwork(ctx context.Context, id string) error {
	if f.removeNetErr != nil {
		return f.removeNetErr
	}
	f.removedNetworks = append(f.removedNetworks, id)
	return nil
}

func TestReattachLeases(t *testing.T) {
	s := NewSerializedStore()
	d := &fakeDiscovery{containers: []DiscoveryContainer{
		{ID: "ctr-1", Name: "svc-alice", Host: "10.0.0.5", Port: 2222,
			Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice", "platform.io/course": "cs101", "platform.io/problem": "hw1"}},
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
			Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice", "platform.io/course": "cs101", "platform.io/problem": "hw1"}},
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
			{ID: "c1", Name: "svc-alice", Host: "h", Port: 22, Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice", "platform.io/course": "cs101", "platform.io/problem": "hw1"}},
		},
		volumes: []DiscoveryVolume{
			{Name: "svc-alice-vol", Driver: "local", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice", "platform.io/course": "cs101", "platform.io/problem": "hw1"}},
			{Name: "svc-orphan-vol", Driver: "local", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "bob", "platform.io/course": "cs102", "platform.io/problem": "hw2"}},
			{Name: "csj-judge-vol", Driver: "local", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "bob", "platform.io/course": "cs102", "platform.io/problem": "hw2"}},
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
			{ID: "c1", Name: "csj-judge-1", Host: "h", Port: 0, Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice", "platform.io/course": "cs101", "platform.io/problem": "hw1"}},
		},
		volumes: []DiscoveryVolume{
			{Name: "csj-vol-1", Driver: "local", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice", "platform.io/course": "cs101", "platform.io/problem": "hw1"}},
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
			{ID: "c1", Name: "svc-alice", Host: "h", Port: 22, Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice", "platform.io/course": "cs101", "platform.io/problem": "hw1"}},
		},
		networks: []DiscoveryNetwork{
			{ID: "n1", Name: "svc-alice-net", Driver: "bridge", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice", "platform.io/course": "cs101", "platform.io/problem": "hw1"}},
			{ID: "n2", Name: "svc-orphan-net", Driver: "bridge", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "charlie", "platform.io/course": "cs103", "platform.io/problem": "hw3"}},
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

func TestSubmitHandlerRejectsUnmappedProblem(t *testing.T) {
	s := NewSerializedStore()
	// No mapping for p1 -> should be rejected
	h := NewHandler(s, nil, &fakeSubmission{})
	body := `{"problem_id":"p1","files":{"a":"YQ=="}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions?course=cs101&contest=c1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unmapped problem, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSubmitHandlerRequiresCourse(t *testing.T) {
	h := NewHandler(NewSerializedStore(), nil, &fakeSubmission{})
	body := `{"problem_id":"p1","files":{"a":"YQ=="}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing course, got %d", rec.Code)
	}
}

func TestReattachReclaimsWithCleaner(t *testing.T) {
	s := NewSerializedStore()
	d := &fakeDiscovery{
		containers: []DiscoveryContainer{
			{ID: "c1", Name: "svc-alice", Host: "h", Port: 22, Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice", "platform.io/course": "cs101", "platform.io/problem": "hw1"}},
		},
		volumes: []DiscoveryVolume{
			{Name: "svc-orphan-1", Driver: "local", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "bob", "platform.io/course": "cs102", "platform.io/problem": "hw2"}},
		},
		networks: []DiscoveryNetwork{
			{ID: "n1", Name: "svc-orphan-net", Driver: "bridge", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "bob", "platform.io/course": "cs102", "platform.io/problem": "hw2"}},
		},
	}
	result, err := ReattachLeases(s, d)
	if err != nil {
		t.Fatalf("ReattachLeases: %v", err)
	}
	if result.ReclaimedVolumes != 1 {
		t.Errorf("ReclaimedVolumes: expected 1, got %d", result.ReclaimedVolumes)
	}
	if result.ReclaimedNets != 1 {
		t.Errorf("ReclaimedNets: expected 1, got %d", result.ReclaimedNets)
	}
	if len(d.removedVolumes) != 1 || d.removedVolumes[0] != "svc-orphan-1" {
		t.Errorf("removedVolumes: %v", d.removedVolumes)
	}
	if len(d.removedNetworks) != 1 || d.removedNetworks[0] != "n1" {
		t.Errorf("removedNetworks: %v", d.removedNetworks)
	}
}

func TestReattachCleanupError(t *testing.T) {
	s := NewSerializedStore()
	d := &fakeDiscovery{
		containers: []DiscoveryContainer{
			{ID: "c1", Name: "svc-alice", Host: "h", Port: 22, Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice", "platform.io/course": "cs101", "platform.io/problem": "hw1"}},
		},
		volumes: []DiscoveryVolume{
			{Name: "svc-orphan-1", Driver: "local", Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "bob", "platform.io/course": "cs102", "platform.io/problem": "hw2"}},
		},
		removeVolErr: fmt.Errorf("volume busy"),
	}
	result, err := ReattachLeases(s, d)
	if err != nil {
		t.Fatalf("ReattachLeases: %v", err)
	}
	// Cleanup error should not block reattach; counts are still incremented
	if result.OrphanVolumes != 1 {
		t.Errorf("OrphanVolumes: %d", result.OrphanVolumes)
	}
	if result.ReclaimedVolumes != 0 {
		t.Errorf("ReclaimedVolumes with error: %d", result.ReclaimedVolumes)
	}
}

// Round 10: Log endpoint regression tests

type fakeLogStreamer struct {
	lastSubID string
	lastCtrID string
	streams   []string
}

func (f *fakeLogStreamer) Submit(ctx context.Context, problemID string, files map[string][]byte) (string, error) {
	return "sub-log-1", nil
}
func (f *fakeLogStreamer) QueryResult(ctx context.Context, submissionID string) (*SubmissionResult, error) {
	return &SubmissionResult{
		SubmissionID: submissionID,
		Status:       "Running",
		Containers:   []ContainerInfo{{ID: "ctr-real-1", Image: "alpine"}},
	}, nil
}
func (f *fakeLogStreamer) StreamLogs(ctx context.Context, submissionID, containerID string, cb func(stream, data string) error) error {
	f.lastSubID = submissionID
	f.lastCtrID = containerID
	f.streams = append(f.streams, "stdout:log-line")
	return cb("stdout", "log-line")
}

func TestLogEndpointRefreshesEmptyCache(t *testing.T) {
	s := NewSerializedStore()
	// Create a just-submitted record with empty Result.
	rec := &SubmissionRecord{ID: "sub-1", ProblemID: "p1", Principal: "alice"}
	s.SaveSubmission(rec)
	streamer := &fakeLogStreamer{}
	h := NewHandler(s, nil, streamer)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/submissions/logs/sub-1", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	// Verify streamer received correct IDs
	if streamer.lastSubID != "sub-1" {
		t.Errorf("submission ID: got %q, want sub-1", streamer.lastSubID)
	}
	if streamer.lastCtrID != "ctr-real-1" {
		t.Errorf("container ID: got %q, want ctr-real-1", streamer.lastCtrID)
	}
}

func TestLogEndpointQueryError(t *testing.T) {
	s := NewSerializedStore()
	rec2 := &SubmissionRecord{ID: "sub-err", ProblemID: "p1", Principal: "alice"}
	s.SaveSubmission(rec2)
	failing := &fakeSubmission{err: fmt.Errorf("CSOJ down")}
	h := NewHandler(s, nil, failing)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/submissions/logs/sub-err", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLogEndpointNoContainers(t *testing.T) {
	s := NewSerializedStore()
	// Record with terminal result but no containers
	rec3 := &SubmissionRecord{ID: "sub-nc", ProblemID: "p1", Principal: "alice",
		Result: SubmissionResult{SubmissionID: "sub-nc", Status: "Success"},
	}
	s.SaveSubmission(rec3)
	emptyResult := &fakeSubmission{result: &SubmissionResult{SubmissionID: "sub-nc", Status: "Success"}}
	h := NewHandler(s, nil, emptyResult)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/submissions/logs/sub-nc", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Round 10: Strict identity regression tests

func TestReattachIncompleteLabelsSameOwner(t *testing.T) {
	s := NewSerializedStore()
	// Alice has active cs102/p2 service
	// Alice has stale volume for cs101/p1 (different course) and missing problem
	d := &fakeDiscovery{
		containers: []DiscoveryContainer{
			{ID: "c1", Name: "svc-alice", Host: "h", Port: 22,
				Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice", "platform.io/course": "cs102", "platform.io/problem": "p2"}},
		},
		volumes: []DiscoveryVolume{
			{Name: "svc-alice-cs101-p1-vol", Driver: "local",
				Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice", "platform.io/course": "cs101", "platform.io/problem": "p1"}},
			{Name: "svc-alice-noproblem-vol", Driver: "local",
				Labels: map[string]string{"platform.io/kind": "service", "platform.io/owner": "alice", "platform.io/course": "cs102"}},
		},
	}
	result, err := ReattachLeases(s, d)
	if err != nil {
		t.Fatalf("ReattachLeases: %v", err)
	}
	if result.Reattached != 1 {
		t.Errorf("reattached: %d", result.Reattached)
	}
	// Both stale volumes should be orphaned
	if result.OrphanVolumes != 2 {
		t.Errorf("orphan volumes: expected 2 (different course + missing problem), got %d", result.OrphanVolumes)
	}
}

// Round 11: Problem sync endpoint tests

type fakeProblemSync struct {
	lastCourse  string
	lastContest string
	lastProblem string
	err         error
	csojID      string
}

func (f *fakeProblemSync) SyncProblem(ctx context.Context, course, contest, problemID, title, startTime, endTime string, cluster string, cpu, memory int, upload map[string]interface{}, workflow []map[string]interface{}, score map[string]interface{}) (string, error) {
	f.lastCourse = course
	f.lastContest = contest
	f.lastProblem = problemID
	if f.err != nil {
		return "", f.err
	}
	if f.csojID != "" {
		return f.csojID, nil
	}
	return contest + "--" + problemID, nil
}

func TestProblemSyncSuccess(t *testing.T) {
	s := NewSerializedStore()
	fs := &fakeProblemSync{csojID: "c1--hw1"}
	h := NewHandlerWithOpts(s, nil, nil, HandlerOpts{ProblemSync: fs})
	body := `{"course":"cs101","contest":"c1","problem_id":"hw1","title":"Homework 1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/problems/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	// Verify mapping was persisted
	if mid := s.ResolveProblem("cs101", "c1", "hw1"); mid != "c1--hw1" {
		t.Errorf("mapping not persisted: got %q", mid)
	}
	if fs.lastContest != "c1" || fs.lastProblem != "hw1" {
		t.Errorf("sync args: contest=%s problem=%s", fs.lastContest, fs.lastProblem)
	}
}

func TestProblemSyncAdapterError(t *testing.T) {
	s := NewSerializedStore()
	fs := &fakeProblemSync{err: fmt.Errorf("CSOJ unreachable")}
	h := NewHandlerWithOpts(s, nil, nil, HandlerOpts{ProblemSync: fs})
	body := `{"course":"cs101","contest":"c1","problem_id":"hw1","title":"HW1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/problems/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestProblemSyncNoMappingStore(t *testing.T) {
	// memStore does not implement MapProblem
	fs := &fakeProblemSync{}
	h := NewHandlerWithOpts(memStore{}, nil, nil, HandlerOpts{ProblemSync: fs})
	body := `{"course":"cs101","contest":"c1","problem_id":"hw1","title":"HW1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/problems/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for missing mapper, got %d", rec.Code)
	}
	if fs.lastProblem != "" {
		t.Fatalf("adapter was called despite missing mapper: lastProblem=%s", fs.lastProblem)
	}
}

func TestProblemSyncDuplicateContests(t *testing.T) {
	s := NewSerializedStore()
	fs := &fakeProblemSync{}
	h := NewHandlerWithOpts(s, nil, nil, HandlerOpts{ProblemSync: fs})
	// Sync p1 in contest c1
	body1 := `{"course":"cs101","contest":"c1","problem_id":"p1","title":"P1"}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/problems/sync", strings.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("c1: %d", rec1.Code)
	}
	// Sync p1 in contest c2 — should map independently
	fs.csojID = "c2--p1"
	body2 := `{"course":"cs101","contest":"c2","problem_id":"p1","title":"P1"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/problems/sync", strings.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("c2: %d", rec2.Code)
	}
	// Verify independent mappings
	if s.ResolveProblem("cs101", "c1", "p1") != "c1--p1" {
		t.Error("c1 mapping wrong")
	}
	if s.ResolveProblem("cs101", "c2", "p1") != "c2--p1" {
		t.Error("c2 mapping wrong")
	}
}

func TestProblemSyncMissingService(t *testing.T) {
	s := NewSerializedStore()
	h := NewHandler(s, nil, nil) // no problem sync service
	body := `{"course":"cs101","contest":"c1","problem_id":"hw1","title":"HW1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/problems/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

// Round 14: FileStore restart test

func TestFileStoreRestart(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/state.json"

	// Create first store and populate data.
	fs1, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Register a key.
	if err := fs1.RegisterKey("alice", "ssh-rsa AAA..."); err != nil {
		t.Fatalf("RegisterKey: %v", err)
	}

	// Create a lease.
	l := lease.NewLease("alice", "ctr-1", "svc-alice", "10.0.0.1", 2222, 8*time.Hour, 30*time.Minute)
	if err := fs1.UpsertLease(l); err != nil {
		t.Fatalf("UpsertLease: %v", err)
	}

	// Save a submission.
	rec := &SubmissionRecord{ID: "sub-1", ProblemID: "p1", Principal: "alice",
		Result: SubmissionResult{SubmissionID: "sub-1", Status: "Success", Score: 100},
	}
	if err := fs1.SaveSubmission(rec); err != nil {
		t.Fatalf("SaveSubmission: %v", err)
	}

	// Map a problem.
	fs1.MapProblem("cs101", "c1", "p1", "c1--p1")

	// Simulate restart: load a new store from the same file.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore (restart): %v", err)
	}

	// Verify key survived.
	key, err := fs2.GetKey("alice")
	if err != nil {
		t.Fatalf("GetKey after restart: %v", err)
	}
	if key != "ssh-rsa AAA..." {
		t.Errorf("key mismatch: %s", key)
	}

	// Verify lease survived.
	l2, err := fs2.LookupByPrincipal("alice")
	if err != nil || l2 == nil {
		t.Fatalf("LookupByPrincipal after restart: %v", err)
	}
	if l2.ContainerID != "ctr-1" {
		t.Errorf("lease container: %s", l2.ContainerID)
	}

	// Verify submission survived.
	sub, err := fs2.GetSubmission("sub-1")
	if err != nil {
		t.Fatalf("GetSubmission after restart: %v", err)
	}
	if sub.Result.Score != 100 {
		t.Errorf("score: %f", sub.Result.Score)
	}

	// Verify problem mapping survived.
	if mid := fs2.ResolveProblem("cs101", "c1", "p1"); mid != "c1--p1" {
		t.Errorf("problem mapping: got %q", mid)
	}
}

// Round 15: Handler restart test — proves keys survive store reopen

func TestHandlerRestartPreservesKey(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/state.json"

	// Phase 1: Register a key with handler using FileStore.
	fs1, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	h1 := NewHandler(fs1, nil, nil)
	body1 := `{"principal":"alice","public_key":"ssh-rsa AAA..."}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/keys", strings.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	h1.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("register key: %d: %s", rec1.Code, rec1.Body.String())
	}

	// Phase 2: Simulate restart — load a new FileStore from same path.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore (restart): %v", err)
	}
	// Verify key survived by reading directly.
	key, err := fs2.GetKey("alice")
	if err != nil {
		t.Fatalf("GetKey after restart: %v", err)
	}
	if key != "ssh-rsa AAA..." {
		t.Errorf("key mismatch: %s", key)
	}

	// Phase 3: New handler with restarted store should see the key.
	h2 := NewHandler(fs2, nil, nil)
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/keys?principal=alice", nil)
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("get key after restart: %d: %s", rec2.Code, rec2.Body.String())
	}
}

// Round 16: MapProblem save error propagation

type errorMapStore struct {
	*serializedStore
	mapErr error
}

func (e *errorMapStore) MapProblem(course, contest, platformID, csojID string) error {
	if e.mapErr != nil {
		return e.mapErr
	}
	return e.serializedStore.MapProblem(course, contest, platformID, csojID)
}

func TestProblemSyncMapProblemError(t *testing.T) {
	s := &errorMapStore{
		serializedStore: NewSerializedStore(),
		mapErr:          fmt.Errorf("disk full"),
	}
	fs := &fakeProblemSync{csojID: "c1--p1"}
	h := NewHandlerWithOpts(s, nil, nil, HandlerOpts{ProblemSync: fs})
	body := `{"course":"cs101","contest":"c1","problem_id":"p1","title":"P1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/problems/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on MapProblem error, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Round 16: Release works with FileStore

func TestReleaseWithFileStore(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/state.json"
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Create a lease for alice.
	l := lease.NewLease("alice", "ctr-1", "svc-alice", "10.0.0.1", 2222, 8*time.Hour, 30*time.Minute)
	if err := fs.UpsertLease(l); err != nil {
		t.Fatalf("UpsertLease: %v", err)
	}

	rt := &fakeRuntime{}
	h := NewHandler(fs, rt, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/release?principal=alice", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Verify container was stopped.
	if rt.stoppedContainerID != "ctr-1" {
		t.Errorf("container not stopped: %s", rt.stoppedContainerID)
	}
	// Verify lease state advanced to Reclaimed.
	final, _ := fs.LookupByPrincipal("alice")
	if final == nil || final.State != lease.StateReclaimed {
		t.Errorf("lease state: %v", final)
	}
}

// Round 17: HTTP-level restart flow tests across FileStore reopen

func TestRestartRegisterKeyUp(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/state.json"

	// Phase 1: register a key with a FileStore-backed handler.
	fs1, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	ca, _ := sshcaSignerForTest()
	h1 := NewHandlerWithOpts(fs1, nil, nil, HandlerOpts{CertSigner: ca})
	keyBody := `{"principal":"alice","public_key":"ssh-ed25519 AAAAtest"}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/keys", strings.NewReader(keyBody))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	h1.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("register-key: %d: %s", rec1.Code, rec1.Body.String())
	}

	// Phase 2: simulate restart — new FileStore from same path, new handler.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore (restart): %v", err)
	}
	h2 := NewHandlerWithOpts(fs2, &fakeRuntime{}, nil, HandlerOpts{CertSigner: ca})

	// Phase 3: POST up should find the persisted key and issue a cert.
	upBody := `{"principal":"alice","image":"img:1","ssh_key":"ssh-ed25519 AAAAtest","course":"cs101","problem":"hw1"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/services", strings.NewReader(upBody))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("up after restart: %d: %s", rec2.Code, rec2.Body.String())
	}
	var up map[string]interface{}
	json.Unmarshal(rec2.Body.Bytes(), &up)
	if _, ok := up["certificate"]; !ok {
		t.Errorf("expected certificate in up response, got: %v", up)
	}
}

func TestRestartSyncSubmit(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/state.json"

	// Phase 1: sync a problem with FileStore-backed handler.
	fs1, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	fs := &fakeProblemSync{csojID: "c1--p1"}
	h1 := NewHandlerWithOpts(fs1, nil, nil, HandlerOpts{ProblemSync: fs})
	syncBody := `{"course":"cs101","contest":"c1","problem_id":"p1","title":"P1"}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/problems/sync", strings.NewReader(syncBody))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	h1.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("sync: %d: %s", rec1.Code, rec1.Body.String())
	}

	// Phase 2: restart — new FileStore, new handler with submission service.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore (restart): %v", err)
	}
	sub := &fakeSubmission{}
	h2 := NewHandlerWithOpts(fs2, nil, sub, HandlerOpts{})

	// Phase 3: submit should resolve the persisted mapping.
	subBody := `{"problem_id":"p1","files":{"main.c":"aW50IG1haW4oKXt9"}}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/submissions?course=cs101&contest=c1", strings.NewReader(subBody))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("submit after restart: %d: %s", rec2.Code, rec2.Body.String())
	}
	if sub.lastProblemID != "c1--p1" {
		t.Errorf("submit used wrong ID: got %q, want c1--p1 (mapped persisted)", sub.lastProblemID)
	}
}

// sshcaSignerForTest returns a CertSigner backed by a freshly generated CA.
func sshcaSignerForTest() (CertSigner, error) {
	// Use a minimal in-process signer that satisfies the CertSigner interface
	// without importing the sshca package (avoid cross-module dep in tests).
	return &stubCertSigner{}, nil
}

type stubCertSigner struct{}

func (s *stubCertSigner) SignUserCert(publicKey, principal string, validityHours int) (string, error) {
	return "ssh-ed25519-cert-v01@openssh.com STUB-CERT-FOR-TEST", nil
}
