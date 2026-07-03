// Package adapter provides the CSOJ Judge Reuse Adapter for hpc101-platform.
//
// The adapter calls CSOJ's HTTP/WebSocket API as the sole integration point.
// CSOJ is zero-change — the adapter maps platform user/problem/submission
// identity into CSOJ's contest/problem/submission model and calls CSOJ as
// a judge, consuming its existing DockerManager/judger pipeline.
//
// Key CSOJ contract details (from vendor/csoj/):
//   - POST /api/v1/problems/:id/submit  — multipart, base64 filenames
//   - GET  /api/v1/submissions/:id       — status: Queued|Running|Success|Failed
//   - WS   /api/v1/ws/submissions/:subID/containers/:conID/logs — NDJSON logs
//   - Result: final workflow step prints {score,performance,info} JSON to stdout;
//     CSOJ persists it; adapter reads it back via status endpoint.
//
// Adapter does NOT write CSOJ's SQLite directly (single-writer constraint).
package adapter

import (
	"context"
)

// Client is the adapter's high-level interface to CSOJ.
// All calls go through CSOJ's HTTP/WebSocket API — no direct DB access.
type Client struct {
	// baseURL is CSOJ's API root, e.g. "http://csoj.hpc101-platform.svc.cluster.local:8080/api/v1"
	baseURL string
	// credential is the internal JWT or service token for CSOJ auth
	credential string
}

// NewClient creates a new adapter client.
// baseURL is CSOJ's API root (e.g., "http://csoj:8080/api/v1").
// credential is the auth token (JWT from CSOJ local login or service account).
func NewClient(baseURL, credential string) *Client {
	return &Client{baseURL: baseURL, credential: credential}
}

// Submission represents a CSOJ submission response.
type Submission struct {
	ID             string       `json:"id"`
	ProblemID      string       `json:"problem_id"`
	UserID         string       `json:"user_id"`
	Status         string       `json:"status"` // Queued, Running, Success, Failed
	Score          float64      `json:"score"`
	Performance    float64      `json:"performance"`
	Info           string       `json:"info"`
	Containers     []Container  `json:"containers,omitempty"`
}

// Container represents a CSOJ judging container.
type Container struct {
	ID      string `json:"id"`
	Image   string `json:"image"`
	Status  string `json:"status"`
	LogPath string `json:"log_path,omitempty"`
}

// Result is the final judging result extracted by the adapter.
// This is the {score, performance, info} JSON that CSOJ's final
// workflow step prints to stdout and the dispatcher persists.
type Result struct {
	Score       float64 `json:"score"`
	Performance float64 `json:"performance"`
	Info        string  `json:"info"`
	Status      string  `json:"status"`
}

// SubmitRequest is the adapter's submission payload.
// The adapter serializes files into CSOJ's multipart form.
type SubmitRequest struct {
	// ProblemID is the platform problem ID (mapped to CSOJ problem).
	ProblemID string
	// Files is a map of base64-encoded relative paths → file content.
	// CSOJ's submission handler decodes filenames from base64.
	// Filenames must pass the problem's upload.files glob rules.
	Files map[string][]byte
}

// Submit sends a submission to CSOJ.
// Returns the CSOJ submission ID on success.
// Implements the base64-filename multipart contract from
// vendor/csoj/internal/api/user/submission.go:162-168.
func (c *Client) Submit(ctx context.Context, req SubmitRequest) (string, error) {
	// TODO: implement HTTP multipart POST with base64-encoded filenames.
	// Authorization: Bearer <c.credential>
	// Content-Type: multipart/form-data
	// URL: POST <baseURL>/problems/<req.ProblemID>/submit
	// Body: each file as multipart field with field name = base64(relative_path)
	return "", nil
}

// QueryResult reads the submission status and result from CSOJ.
// Returns the full Submission record including score/performance/info.
func (c *Client) QueryResult(ctx context.Context, submissionID string) (*Submission, error) {
	// TODO: implement HTTP GET
	// URL: GET <baseURL>/submissions/<submissionID>
	// Returns full submission including status, score, performance, info
	return nil, nil
}

// StreamLogs opens a WebSocket connection to stream the judge container logs.
// callback receives each NDJSON line: {"stream":"stdout","data":"..."} or
// {"stream":"stderr","data":"..."}.
// This mirrors CSOJ-cli's stream_logs function and CSOJ-WebUI's log viewer.
// URL: WS <baseURL>/ws/submissions/<subID>/containers/<conID>/logs?token=<jwt>
func (c *Client) StreamLogs(ctx context.Context, submissionID, containerID string, callback func(stream, data string) error) error {
	// TODO: implement WebSocket connection with gorilla/websocket
	// Parse NDJSON frames, call callback for each line
	return nil
}

// ContestRecord represents the CSOJ-side contest/problem registration
// the adapter must maintain for the platform's problem catalog.
type ContestRecord struct {
	ContestID  string `json:"contest_id"`
	ProblemID  string `json:"problem_id"`
	Title      string `json:"title"`
	StartTime  string `json:"start_time"`
	EndTime    string `json:"end_time"`
}

// SyncProblem ensures the platform problem has a corresponding CSOJ
// contest/problem record. Called when a course problem is published.
// Uses CSOJ's admin API (requires admin credential).
func (c *Client) SyncProblem(ctx context.Context, rec ContestRecord) error {
	// TODO: upsert contest/problem via CSOJ admin API or filesystem
	return nil
}
