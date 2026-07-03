package adapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubmitBase64Filenames(t *testing.T) {
	// Mock CSOJ server that asserts the multipart contract
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify method and auth
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing bearer token")
		}

		// Parse multipart — CSOJ uses r.FormFile("files")
		err := r.ParseMultipartForm(10 << 20)
		if err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}

		// CSOJ reads all "files" fields: r.MultipartForm.File["files"]
		files := r.MultipartForm.File["files"]
		if len(files) == 0 {
			http.Error(w, `{"code":1,"message":"no files"}`, 400)
			return
		}

		// Each file's Filename is base64(relative_path). CSOJ base64-decodes it.
		// We verify the encoded filename round-trips correctly.
		decodedNames := make([]string, 0, len(files))
		for _, fh := range files {
			decoded, err := base64.StdEncoding.DecodeString(fh.Filename)
			if err != nil {
				t.Errorf("filename not valid base64: %s", fh.Filename)
			}
			decodedNames = append(decodedNames, string(decoded))
		}
		t.Logf("decoded filenames: %v", decodedNames)

		// Return CSOJ's success envelope
		resp := map[string]interface{}{
			"code":    0,
			"message": "success",
			"data": map[string]interface{}{
				"submission_id": "sub-abc123",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	id, err := client.Submit(context.Background(), SubmitRequest{
		ProblemID: "problem-1",
		Files: map[string][]byte{
			"src/main.c":    []byte("#include <stdio.h>\nint main(){}"),
			"src/Makefile":  []byte("all:\n\tgcc main.c\n"),
		},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if id != "sub-abc123" {
		t.Errorf("expected sub-abc123, got %s", id)
	}
}

func TestSubmitRejectsCSOJError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"code":    1,
			"message": "contest not found",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	_, err := client.Submit(context.Background(), SubmitRequest{
		ProblemID: "bad-problem",
		Files:     map[string][]byte{"test.txt": []byte("x")},
	})
	if err == nil {
		t.Fatal("expected error for CSOJ rejection, got nil")
	}
	if !strings.Contains(err.Error(), "contest not found") {
		t.Errorf("error should contain CSOJ message: %v", err)
	}
}

func TestQueryResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/submissions/sub-xyz") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := map[string]interface{}{
			"code":    0,
			"message": "success",
			"data": map[string]interface{}{
				"id":          "sub-xyz",
				"problem_id":  "problem-1",
				"user_id":     "student-42",
				"status":      "Success",
				"score":       100.0,
				"performance": 95.5,
				"info":        "All tests passed",
				"containers": []interface{}{
					map[string]interface{}{
						"id":     "ctr-1",
						"image":  "gcc:latest",
						"status": "Finished",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	sub, err := client.QueryResult(context.Background(), "sub-xyz")
	if err != nil {
		t.Fatalf("QueryResult: %v", err)
	}
	if sub.Status != "Success" {
		t.Errorf("status: got %s", sub.Status)
	}
	if sub.Score != 100.0 {
		t.Errorf("score: got %f", sub.Score)
	}
	if sub.Performance != 95.5 {
		t.Errorf("performance: got %f", sub.Performance)
	}
	if sub.Info != "All tests passed" {
		t.Errorf("info: got %s", sub.Info)
	}
	if len(sub.Containers) != 1 || sub.Containers[0].ID != "ctr-1" {
		t.Errorf("containers: %+v", sub.Containers)
	}
}

func TestStreamLogsNotImplemented(t *testing.T) {
	client := NewClient("http://localhost", "x")
	err := client.StreamLogs(context.Background(), "s", "c", nil)
	if err == nil {
		t.Fatal("expected 'not implemented' error")
	}
}

func TestSyncProblemNotImplemented(t *testing.T) {
	client := NewClient("http://localhost", "x")
	err := client.SyncProblem(context.Background(), ContestRecord{})
	if err == nil {
		t.Fatal("expected 'not implemented' error")
	}
}
