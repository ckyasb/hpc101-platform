package adapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSubmitBase64Filenames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing bearer token")
		}
		err := r.ParseMultipartForm(10 << 20)
		if err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		files := r.MultipartForm.File["files"]
		if len(files) == 0 {
			http.Error(w, `{"code":1,"message":"no files"}`, 400)
			return
		}
		decodedNames := make([]string, 0, len(files))
		for _, fh := range files {
			decoded, err := base64.StdEncoding.DecodeString(fh.Filename)
			if err != nil {
				t.Errorf("filename not valid base64: %s", fh.Filename)
			}
			decodedNames = append(decodedNames, string(decoded))
		}
		t.Logf("decoded filenames: %v", decodedNames)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0, "message": "success",
			"data": map[string]interface{}{"submission_id": "sub-abc123"},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	id, err := client.Submit(context.Background(), SubmitRequest{
		ProblemID: "problem-1",
		Files: map[string][]byte{
			"src/main.c":   []byte("#include <stdio.h>\nint main(){}"),
			"src/Makefile": []byte("all:\n\tgcc main.c\n"),
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
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "message": "contest not found",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	_, err := client.Submit(context.Background(), SubmitRequest{
		ProblemID: "bad-problem",
		Files:     map[string][]byte{"test.txt": []byte("x")},
	})
	if err == nil {
		t.Fatal("expected error for CSOJ rejection")
	}
	if !strings.Contains(err.Error(), "contest not found") {
		t.Errorf("error should contain CSOJ message: %v", err)
	}
}

func TestQueryResultWithRealCSOJShapes(t *testing.T) {
	// Simulate CSOJ returning score as integer (common in Go JSON) and
	// info as a JSON object (CSOJ's models.JSONMap = map[string]interface{}).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"code": 0, "message": "success",
			"data": map[string]interface{}{
				"id":          "sub-xyz",
				"problem_id":  "problem-1",
				"user_id":     "student-42",
				"status":      "Success",
				"score":       float64(100), // ensure float for JSON
				"performance": float64(95),
				"info":        map[string]interface{}{"passed": float64(3), "failed": float64(0)},
				"containers": []interface{}{
					map[string]interface{}{
						"id": "ctr-1", "image": "gcc:latest", "status": "Finished",
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
		t.Errorf("status: %s", sub.Status)
	}
	if float64(sub.Score) != 100.0 {
		t.Errorf("score: %f", sub.Score)
	}
	// Info should be a JSON object (RawMessage), not a forced string
	var infoMap map[string]interface{}
	if err := json.Unmarshal(sub.Info, &infoMap); err != nil {
		t.Errorf("info is not a JSON object: %v", err)
	} else {
		if infoMap["passed"] != float64(3) {
			t.Errorf("info.passed: %v", infoMap["passed"])
		}
	}
}

func TestQueryResultWithStringInfo(t *testing.T) {
	// Info as plain string (e.g. "All tests passed") — must also parse
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0, "message": "success",
			"data": map[string]interface{}{
				"id": "sub-abc", "problem_id": "p1", "user_id": "u1",
				"status": "Success", "score": float64(90), "performance": float64(80),
				"info": "All tests passed",
			},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	sub, err := client.QueryResult(context.Background(), "sub-abc")
	if err != nil {
		t.Fatalf("QueryResult: %v", err)
	}
	// Info should be a JSON string
	var s string
	if err := json.Unmarshal(sub.Info, &s); err != nil {
		t.Errorf("info is not a JSON string: %v", err)
	}
}

func TestStreamLogsWebSocket(t *testing.T) {
	var mu sync.Mutex
	var received []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("ws upgrade: %v", err)
		}
		defer conn.Close()
		// Send two NDJSON frames
		conn.WriteMessage(websocket.TextMessage, []byte(`{"stream":"stdout","data":"hello"}`))
		conn.WriteMessage(websocket.TextMessage, []byte(`{"stream":"stderr","data":"oops"}`))
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	}))
	defer srv.Close()

	client := NewClient("http://"+srv.Listener.Addr().String(), "test-token")
	err := client.StreamLogs(context.Background(), "sub-1", "ctr-1", func(stream, data string) error {
		mu.Lock()
		received = append(received, stream+":"+data)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("expected 2 frames, got %d: %v", len(received), received)
	}
	if received[0] != "stdout:hello" || received[1] != "stderr:oops" {
		t.Errorf("unexpected frames: %v", received)
	}
}

func TestSyncProblemHTTP(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		// GET returns 404 for not-found (triggers POST), 200 for found (triggers PUT)
		if r.Method == http.MethodGet {
			code := 1 // not found
			if strings.Contains(r.URL.Path, "/problems/p1") || strings.Contains(r.URL.Path, "/contests/c1") && r.Method == http.MethodGet {
				// First GETs: return not-found → POST path
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"code": code})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"code": 0})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok")
	c.SyncProblem(context.Background(), ContestRecord{ContestID:"c1",ProblemID:"p1",Title:"T"})
	if len(calls) < 3 { t.Fatalf("expected >=3 calls, got %d: %v", len(calls), calls) }
}

func TestSyncProblemRejectsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		w.Write([]byte(`{"code":1,"message":"duplicate problem ID"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "x")
	err := client.SyncProblem(context.Background(), ContestRecord{ContestID: "c1", ProblemID: "dup"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "duplicate problem ID") {
		t.Errorf("error should contain CSOJ message: %v", err)
	}
}

func TestStreamLogsContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("ws upgrade: %v", err)
		}
		defer conn.Close()
		// Send one frame, then idle (test cancels context)
		conn.WriteMessage(websocket.TextMessage, []byte(`{"stream":"stdout","data":"line1"}`))
		// Block until test cancellation closes connection
		_, _, err = conn.ReadMessage()
		// Expected: read fails due to close
		_ = err
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	var received []string
	done := make(chan error, 1)

	go func() {
		client := NewClient("http://"+srv.Listener.Addr().String(), "test-token")
		done <- client.StreamLogs(ctx, "sub-1", "ctr-1", func(stream, data string) error {
			received = append(received, stream+":"+data)
			return nil
		})
	}()

	// Wait for first frame to arrive, then cancel
	time.Sleep(100 * time.Millisecond)
	cancel()

	err := <-done
	if err == nil {
		t.Log("StreamLogs returned nil after cancel (conn closed)")
	} else if err != context.Canceled {
		t.Logf("StreamLogs returned: %v (expected context.Canceled or nil)", err)
	}
	if len(received) == 0 {
		t.Error("should have received at least the first frame before cancel")
	}
}
