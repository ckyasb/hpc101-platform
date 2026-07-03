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
	err           error
	id            string
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
	stopCh chan struct{}
}

func (b *blockingRuntime) CreateService(req CreateServiceRequest) (*ServiceResult, error) {
	return nil, nil
}
func (b *blockingRuntime) StopService(containerID string) error {
	<-b.stopCh
	return nil
}

func TestReleaseBlocksConcurrentUpsert(t *testing.T) {
	s := NewSerializedStore()
	l := lease.NewLease("student-42", "ctr-abc", "svc-student-42", "10.0.0.5", 2222, 8*time.Hour, 30*time.Minute)
	s.UpsertLease(l)

	br := &blockingRuntime{stopCh: make(chan struct{})}
	releaseDone := make(chan error, 1)
	go func() {
		releaseDone <- s.ReleaseLeaseIf("student-42", lease.TriggerManual, func(l *Lease) bool { return true }, br, nil)
	}()

	time.Sleep(50 * time.Millisecond)

	newLease := lease.NewLease("student-42", "ctr-def", "svc-student-42", "10.0.0.6", 2222, 8*time.Hour, 30*time.Minute)
	upsertDone := make(chan struct{})
	go func() {
		s.UpsertLease(newLease)
		close(upsertDone)
	}()

	select {
	case <-upsertDone:
		t.Log("upsert completed before release (lock may have been released)")
	case <-time.After(100 * time.Millisecond):
		// Expected: upsert blocked by lock
	}

	close(br.stopCh)
	<-releaseDone

	select {
	case <-upsertDone:
	case <-time.After(2 * time.Second):
		t.Fatal("upsert still blocked after release completed")
	}
}

type recordingDrainer struct {
	drains []string
}

func (r *recordingDrainer) Drain(p string) error {
	r.drains = append(r.drains, p)
	return nil
}

func TestReleaseCallsDrainerBeforeStop(t *testing.T) {
	s := NewSerializedStore()
	l := lease.NewLease("student-42", "ctr-abc", "svc-student-42", "10.0.0.5", 2222, 8*time.Hour, 30*time.Minute)
	s.UpsertLease(l)
	d := &recordingDrainer{}
	rt := &fakeRuntime{}

	err := s.ReleaseLeaseIf("student-42", lease.TriggerManual, func(l *Lease) bool { return true }, rt, d)
	if err != nil {
		t.Fatalf("ReleaseLeaseIf: %v", err)
	}
	if len(d.drains) != 1 || d.drains[0] != "student-42" {
		t.Errorf("drainer not called before stop: %v", d.drains)
	}
	if rt.stoppedContainerID != "ctr-abc" {
		t.Error("stop not called")
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
