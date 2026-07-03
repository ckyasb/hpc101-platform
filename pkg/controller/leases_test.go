package controller

import (
	"encoding/json"
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
	lastPrincipal string
	lastImage     string
	lastCourse    string
	lastProblem   string
}

func (f *fakeRuntime) CreateService(principal, image, sshKey, course, problem string) (*ServiceResult, error) {
	f.lastPrincipal = principal
	f.lastImage = image
	f.lastCourse = course
	f.lastProblem = problem
	return &ServiceResult{ContainerID: "ctr-" + principal, Host: "10.0.0.5", Port: 2222}, nil
}

func TestHandleLeasesActive(t *testing.T) {
	l := lease.NewLease("student-42", "abc", "svc-student-42", "10.0.0.5", 2222, 8*time.Hour, 30*time.Minute)
	store := memStore{"student-42": l}
	h := NewHandler(store, nil)

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
	h := NewHandler(memStore{}, nil)

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
	h := NewHandler(store, rt)

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

func TestHandleLeasesNoActiveLease(t *testing.T) {
	h := NewHandler(memStore{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/leases?principal=nobody", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for no lease, got %d", rec.Code)
	}
}
