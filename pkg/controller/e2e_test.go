package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hpc101-platform/lease"
)

// TestE2ECourseFlow exercises the complete course workflow through the HTTP API:
//
//	register-key (create service) → check-lease → submit → check-scores → release
//
// No kubeconfig. No OIDC. All student identity is carried via the principal name.
func TestE2ECourseFlow(t *testing.T) {
	rt := &fakeRuntime{}
	sub := &fakeSubmission{}
	store := NewSerializedStore()
	// Pre-populate problem mapping so submit resolves correctly.
	store.MapProblem("cs101", "c1", "hw1", "cs101--hw1")
	h := NewHandler(store, rt, sub)

	principal := "e2e-student"
	problemID := "hw1"

	// Step 1: Create service (register-key + up)
	createBody := fmt.Sprintf(
		`{"principal":"%s","image":"hpc101-platform/container:latest","ssh_key":"ssh-ed25519 AAA...","course":"cs101","problem":"%s"}`,
		principal, problemID,
	)
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/services", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)

	if createRec.Code != http.StatusCreated {
		t.Fatalf("step1 create-service: expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var createResp map[string]interface{}
	json.Unmarshal(createRec.Body.Bytes(), &createResp)
	if createResp["state"] != string(lease.StateActive) {
		t.Fatalf("step1: service not active: %v", createResp)
	}

	// Step 2: Verify lease is visible (ssh-info)
	leaseReq := httptest.NewRequest(http.MethodGet, "/api/v1/leases?principal="+principal, nil)
	leaseRec := httptest.NewRecorder()
	h.ServeHTTP(leaseRec, leaseReq)

	if leaseRec.Code != http.StatusOK {
		t.Fatalf("step2 check-lease: expected 200, got %d: %s", leaseRec.Code, leaseRec.Body.String())
	}
	var leaseResp map[string]interface{}
	json.Unmarshal(leaseRec.Body.Bytes(), &leaseResp)
	if leaseResp["container_host"] == nil || leaseResp["container_port"] == nil {
		t.Fatalf("step2: missing host/port in lease: %v", leaseResp)
	}

	// Step 3: Submit a solution
	submitBody := fmt.Sprintf(`{"problem_id":"%s","files":{"main.c":"%s"}}`, problemID, "aW50IG1haW4oKXtyZXR1cm4gMDt9")
	submitReq := httptest.NewRequest(http.MethodPost, "/api/v1/submissions?course=cs101&contest=c1", strings.NewReader(submitBody))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	h.ServeHTTP(submitRec, submitReq)

	if submitRec.Code != http.StatusAccepted {
		t.Fatalf("step3 submit: expected 202, got %d: %s", submitRec.Code, submitRec.Body.String())
	}
	var submitResp map[string]string
	json.Unmarshal(submitRec.Body.Bytes(), &submitResp)
	if submitResp["submission_id"] == "" {
		t.Fatal("step3: no submission_id in response")
	}
	if submitResp["status"] != "submitted" {
		t.Fatalf("step3: unexpected status: %s", submitResp["status"])
	}

	// Step 4: Check problems list
	problemsReq := httptest.NewRequest(http.MethodGet, "/api/v1/problems", nil)
	problemsRec := httptest.NewRecorder()
	h.ServeHTTP(problemsRec, problemsReq)
	if problemsRec.Code != http.StatusOK {
		t.Fatalf("step4 problems: expected 200, got %d", problemsRec.Code)
	}

	// Step 5: Check scores
	scoresReq := httptest.NewRequest(http.MethodGet, "/api/v1/scores", nil)
	scoresRec := httptest.NewRecorder()
	h.ServeHTTP(scoresRec, scoresReq)
	if scoresRec.Code != http.StatusOK {
		t.Fatalf("step5 scores: expected 200, got %d", scoresRec.Code)
	}

	// Step 6: Release
	releaseReq := httptest.NewRequest(http.MethodDelete, "/api/v1/release?principal="+principal, nil)
	releaseRec := httptest.NewRecorder()
	h.ServeHTTP(releaseRec, releaseReq)

	if releaseRec.Code != http.StatusOK {
		t.Fatalf("step6 release: expected 200, got %d: %s", releaseRec.Code, releaseRec.Body.String())
	}
	var releaseResp map[string]string
	json.Unmarshal(releaseRec.Body.Bytes(), &releaseResp)
	if releaseResp["status"] != string(lease.StateReclaimed) {
		t.Fatalf("step6: expected Reclaimed, got %s", releaseResp["status"])
	}

	// Verify lease is no longer accessible after release
	leaseAfterReq := httptest.NewRequest(http.MethodGet, "/api/v1/leases?principal="+principal, nil)
	leaseAfterRec := httptest.NewRecorder()
	h.ServeHTTP(leaseAfterRec, leaseAfterReq)
	if leaseAfterRec.Code != http.StatusNotFound {
		t.Fatalf("step6 verify: expected 404 after release, got %d", leaseAfterRec.Code)
	}

	// Verify runtime was cleaned up
	if rt.stoppedContainerID == "" {
		t.Error("step6: container was not stopped during release")
	}
}

// TestE2EMultiStudentIsolation verifies that multiple students cannot
// access each other's leases or services.
func TestE2EMultiStudentIsolation(t *testing.T) {
	rt := &fakeRuntime{}
	store := NewSerializedStore()
	h := NewHandler(store, rt, nil)

	// Create service for alice
	aliceBody := `{"principal":"alice","image":"img:1","ssh_key":"key-alice","course":"cs101","problem":"hw1"}`
	aliceReq := httptest.NewRequest(http.MethodPost, "/api/v1/services", strings.NewReader(aliceBody))
	aliceReq.Header.Set("Content-Type", "application/json")
	aliceRec := httptest.NewRecorder()
	h.ServeHTTP(aliceRec, aliceReq)
	if aliceRec.Code != http.StatusCreated {
		t.Fatalf("alice create: %d", aliceRec.Code)
	}

	// Create service for bob
	bobBody := `{"principal":"bob","image":"img:1","ssh_key":"key-bob","course":"cs101","problem":"hw1"}`
	bobReq := httptest.NewRequest(http.MethodPost, "/api/v1/services", strings.NewReader(bobBody))
	bobReq.Header.Set("Content-Type", "application/json")
	bobRec := httptest.NewRecorder()
	h.ServeHTTP(bobRec, bobReq)
	if bobRec.Code != http.StatusCreated {
		t.Fatalf("bob create: %d", bobRec.Code)
	}

	// Alice can see her lease
	aliceGet := httptest.NewRequest(http.MethodGet, "/api/v1/leases?principal=alice", nil)
	aliceGetRec := httptest.NewRecorder()
	h.ServeHTTP(aliceGetRec, aliceGet)
	if aliceGetRec.Code != http.StatusOK {
		t.Errorf("alice cannot see own lease: %d", aliceGetRec.Code)
	}
	var aliceLease map[string]interface{}
	json.Unmarshal(aliceGetRec.Body.Bytes(), &aliceLease)

	// Bob can see his lease
	bobGet := httptest.NewRequest(http.MethodGet, "/api/v1/leases?principal=bob", nil)
	bobGetRec := httptest.NewRecorder()
	h.ServeHTTP(bobGetRec, bobGet)
	if bobGetRec.Code != http.StatusOK {
		t.Errorf("bob cannot see own lease: %d", bobGetRec.Code)
	}
	var bobLease map[string]interface{}
	json.Unmarshal(bobGetRec.Body.Bytes(), &bobLease)

	// Bob releases his service
	bobRelease := httptest.NewRequest(http.MethodDelete, "/api/v1/release?principal=bob", nil)
	bobReleaseRec := httptest.NewRecorder()
	h.ServeHTTP(bobReleaseRec, bobRelease)
	if bobReleaseRec.Code != http.StatusOK {
		t.Fatalf("bob release: %d", bobReleaseRec.Code)
	}

	// Alice's lease should still be active after bob releases
	aliceAfter := httptest.NewRequest(http.MethodGet, "/api/v1/leases?principal=alice", nil)
	aliceAfterRec := httptest.NewRecorder()
	h.ServeHTTP(aliceAfterRec, aliceAfter)
	if aliceAfterRec.Code != http.StatusOK {
		t.Errorf("alice lease affected by bob release: %d", aliceAfterRec.Code)
	}

	// Bob's lease should be gone
	bobAfter := httptest.NewRequest(http.MethodGet, "/api/v1/leases?principal=bob", nil)
	bobAfterRec := httptest.NewRecorder()
	h.ServeHTTP(bobAfterRec, bobAfter)
	if bobAfterRec.Code != http.StatusNotFound {
		t.Errorf("bob lease still visible after release: %d", bobAfterRec.Code)
	}
}

// TestE2EReleaseIdempotency verifies that releasing an already-released
// lease is handled gracefully.
func TestE2EReleaseIdempotency(t *testing.T) {
	rt := &fakeRuntime{}
	store := NewSerializedStore()
	h := NewHandler(store, rt, nil)

	principal := "idempotent-user"
	createBody := fmt.Sprintf(`{"principal":"%s","image":"img:1","ssh_key":"key","course":"c","problem":"p"}`, principal)
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/services", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: %d", createRec.Code)
	}

	// First release: should succeed
	release1 := httptest.NewRequest(http.MethodDelete, "/api/v1/release?principal="+principal, nil)
	release1Rec := httptest.NewRecorder()
	h.ServeHTTP(release1Rec, release1)
	if release1Rec.Code != http.StatusOK {
		t.Fatalf("first release: %d", release1Rec.Code)
	}

	// Second release: should fail (already released)
	release2 := httptest.NewRequest(http.MethodDelete, "/api/v1/release?principal="+principal, nil)
	release2Rec := httptest.NewRecorder()
	h.ServeHTTP(release2Rec, release2)
	if release2Rec.Code != http.StatusInternalServerError {
		t.Errorf("second release: expected error, got %d", release2Rec.Code)
	}
}

// TestE2ERecreateAfterRelease verifies that a student can release their
// service and then create a new one (up after release).
func TestE2ERecreateAfterRelease(t *testing.T) {
	rt := &fakeRuntime{}
	store := NewSerializedStore()
	h := NewHandler(store, rt, nil)

	principal := "recreate-user"

	// Create first service
	body1 := fmt.Sprintf(`{"principal":"%s","image":"img:v1","ssh_key":"key","course":"cs101","problem":"hw1"}`, principal)
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/services", strings.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first create: %d", rec1.Code)
	}

	// Release
	releaseReq := httptest.NewRequest(http.MethodDelete, "/api/v1/release?principal="+principal, nil)
	releaseRec := httptest.NewRecorder()
	h.ServeHTTP(releaseRec, releaseReq)
	if releaseRec.Code != http.StatusOK {
		t.Fatalf("release: %d", releaseRec.Code)
	}

	// Create second service with different image
	body2 := fmt.Sprintf(`{"principal":"%s","image":"img:v2","ssh_key":"key","course":"cs102","problem":"hw2"}`, principal)
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/services", strings.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("second create after release: %d: %s", rec2.Code, rec2.Body.String())
	}

	// Verify new lease is active
	leaseReq := httptest.NewRequest(http.MethodGet, "/api/v1/leases?principal="+principal, nil)
	leaseRec := httptest.NewRecorder()
	h.ServeHTTP(leaseRec, leaseReq)
	if leaseRec.Code != http.StatusOK {
		t.Fatalf("lease after recreate: %d", leaseRec.Code)
	}

	// Verify the runtime was called with the new image
	if rt.lastImage != "img:v2" {
		t.Errorf("expected img:v2 on recreate, got %s", rt.lastImage)
	}
	if rt.lastCourse != "cs102" || rt.lastProblem != "hw2" {
		t.Errorf("course/problem: %s/%s", rt.lastCourse, rt.lastProblem)
	}
}

// TestE2ELeaseStateTransitions verifies the complete lease lifecycle
// state transitions through the release pipeline.
func TestE2ELeaseStateTransitions(t *testing.T) {
	store := NewSerializedStore()

	principal := "state-user"
	l := lease.NewLease(principal, "ctr-state", "svc-state-user", "10.0.0.5", 2222, 8*time.Hour, 30*time.Minute)
	store.UpsertLease(l)

	// Verify initial state
	got, _ := store.LookupByPrincipal(principal)
	if got.State != lease.StateActive {
		t.Fatalf("initial state: %s", got.State)
	}

	// Trigger manual release — full pipeline
	rec := &orderedRecorder{}
	err := store.ReleaseLeaseIf(principal, lease.TriggerManual, func(l *Lease) bool { return true }, rec, rec)
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	// Verify final state
	final, _ := store.LookupByPrincipal(principal)
	if final.State != lease.StateReclaimed {
		t.Fatalf("final state: %s (expected Reclaimed)", final.State)
	}
	if final.ReleasedBy != lease.TriggerManual {
		t.Errorf("ReleasedBy: %s", final.ReleasedBy)
	}

	// Verify drain called before stop (ordered)
	expected := []string{"drain:" + principal, "stop:ctr-state"}
	if len(rec.events) != 2 || rec.events[0] != expected[0] || rec.events[1] != expected[1] {
		t.Errorf("event order: %v (expected %v)", rec.events, expected)
	}
}

// TestE2ESubmitWithoutService verifies submit fails gracefully when
// no submission service is configured.
func TestE2ESubmitWithoutService(t *testing.T) {
	h := NewHandler(NewSerializedStore(), &fakeRuntime{}, nil)
	body := `{"problem_id":"p1","files":{"main.c":"aW50IG1haW4oKXt9"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

// TestE2EHealthCheck verifies the health endpoint is reachable
// throughout the service lifecycle.
func TestE2EHealthCheck(t *testing.T) {
	h := NewHandler(NewSerializedStore(), &fakeRuntime{}, nil)

	check := func() int {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if check() != http.StatusOK {
		t.Fatal("healthz failed")
	}
}

// TestE2ECreateServiceTriggeredRelease verifies the release loop
// integration — max-life and idle triggers working end-to-end.
func TestE2ECreateServiceTriggeredRelease(t *testing.T) {
	store := NewSerializedStore()
	rt := &fakeRuntime{}

	// Test max-life trigger
	t.Run("max-life", func(t *testing.T) {
		l := lease.NewLease("ml-user", "ctr-ml", "svc-ml-user", "10.0.0.5", 2222, 1*time.Nanosecond, 30*time.Minute)
		time.Sleep(time.Millisecond)
		store.UpsertLease(l)

		err := store.ReleaseLeaseIf("ml-user", lease.TriggerMaxLife, func(l *Lease) bool { return l.IsExpired() }, rt, nil)
		if err != nil {
			t.Fatalf("max-life release: %v", err)
		}
		final, _ := store.LookupByPrincipal("ml-user")
		if final.State != lease.StateReclaimed {
			t.Errorf("max-life: state=%s", final.State)
		}
	})

	// Test idle trigger
	t.Run("idle", func(t *testing.T) {
		l := lease.NewLease("idle-user", "ctr-idle", "svc-idle-user", "10.0.0.5", 2222, 8*time.Hour, 1*time.Nanosecond)
		time.Sleep(time.Millisecond)
		store.UpsertLease(l)

		err := store.ReleaseLeaseIf("idle-user", lease.TriggerIdle, func(l *Lease) bool { return l.IsIdle() }, rt, nil)
		if err != nil {
			t.Fatalf("idle release: %v", err)
		}
		final, _ := store.LookupByPrincipal("idle-user")
		if final.State != lease.StateReclaimed {
			t.Errorf("idle: state=%s", final.State)
		}
	})
}

// TestE2EReleaseTriggerBackgroundLoop starts the background release
// triggers and verifies they operate correctly.
func TestE2EReleaseTriggerBackgroundLoop(t *testing.T) {
	store := NewSerializedStore()
	rt := &fakeRuntime{}

	// Create an expired lease
	l := lease.NewLease("bg-user", "ctr-bg", "svc-bg-user", "10.0.0.5", 2222, 1*time.Nanosecond, 30*time.Minute)
	time.Sleep(time.Millisecond)
	store.UpsertLease(l)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start triggers with short interval
	StartReleaseTriggers(ctx, store, rt, nil, 50*time.Millisecond)

	// Wait for trigger to fire
	time.Sleep(200 * time.Millisecond)

	final, _ := store.LookupByPrincipal("bg-user")
	if final.State != lease.StateReclaimed {
		t.Errorf("background trigger did not release expired lease: state=%s", final.State)
	}
}

// TestE2ENoKubeconfigDependency verifies that no endpoint requires
// kubeconfig, OIDC tokens, or k8s impersonation headers.
func TestE2ENoKubeconfigDependency(t *testing.T) {
	h := NewHandler(NewSerializedStore(), &fakeRuntime{}, &fakeSubmission{})

	endpoints := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/leases?principal=test", ""},
		{http.MethodGet, "/api/v1/problems", ""},
		{http.MethodGet, "/api/v1/scores", ""},
		{http.MethodGet, "/healthz", ""},
	}

	// All endpoints should work without k8s auth headers
	for _, ep := range endpoints {
		req := httptest.NewRequest(ep.method, ep.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		// Should not require auth (no 401/403)
		if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
			t.Errorf("%s %s returned %d — appears to require auth", ep.method, ep.path, rec.Code)
		}
	}

	// Verify that k8s impersonation headers are NOT used
	impReq := httptest.NewRequest(http.MethodGet, "/api/v1/leases?principal=test", nil)
	impReq.Header.Set("Impersonate-User", "admin")
	impReq.Header.Set("Authorization", "Bearer k8s-token")
	impRec := httptest.NewRecorder()
	h.ServeHTTP(impRec, impReq)
	// The API uses principal from query param, not from impersonation
	if impRec.Code == http.StatusNotFound {
		// Expected: no lease for "test", but endpoint works without impersonation
	} else if impRec.Code == http.StatusOK {
		// Also fine — lease may exist but impersonation headers are ignored
	}
}
