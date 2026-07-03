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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
)

// Client is the adapter's high-level interface to CSOJ.
type Client struct {
	baseURL    string
	credential string
	httpClient *http.Client
}

// NewClient creates a new adapter client.
// baseURL is CSOJ's API root (e.g., "http://csoj:8080/api/v1").
func NewClient(baseURL, credential string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		credential: credential,
		httpClient: new(http.Client),
	}
}

// --- Types ---

// csjResponse is the CSOJ API envelope: {code, message, data}.
type csjResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// CSOJError is a structured error from a CSOJ API call.
type CSOJError struct {
	Method     string
	Path       string
	HTTPStatus int
	Code       int
	Message    string
}

func (e *CSOJError) Error() string {
	if e.HTTPStatus == 404 {
		return fmt.Sprintf("adapter: %s %s not found", e.Method, e.Path)
	}
	return fmt.Sprintf("adapter: %s %s HTTP %d (code=%d): %s", e.Method, e.Path, e.HTTPStatus, e.Code, e.Message)
}

func newCSOJError(method, path string, status int, env csjResponse) *CSOJError {
	return &CSOJError{Method: method, Path: path, HTTPStatus: status, Code: env.Code, Message: env.Message}
}

// validateEnvelope checks HTTP status and CSOJ envelope code.
// Returns the parsed response on success, or *CSOJError on failure.
func validateEnvelope(method, path string, resp *http.Response, body []byte) (*csjResponse, error) {
	var env csjResponse
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("adapter: parse %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || env.Code != 0 {
		return nil, newCSOJError(method, path, resp.StatusCode, env)
	}
	return &env, nil
}

// CSOJSONNumber handles CSOJ scores that may be encoded as int or float64.
type CSOJSONNumber float64

func (n *CSOJSONNumber) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*n = CSOJSONNumber(f)
	return nil
}

// Submission represents a CSOJ submission record.
// Info uses json.RawMessage because CSOJ returns models.JSONMap
// (map[string]interface{}) which may be a JSON object or string.
type Submission struct {
	ID          string          `json:"id"`
	ProblemID   string          `json:"problem_id"`
	UserID      string          `json:"user_id"`
	Status      string          `json:"status"` // Queued, Running, Success, Failed
	Score       CSOJSONNumber   `json:"score"`
	Performance CSOJSONNumber   `json:"performance"`
	Info        json.RawMessage `json:"info"`
	Containers  []Container     `json:"containers,omitempty"`
}

// Container represents a CSOJ judging container.
type Container struct {
	ID      string `json:"id"`
	Image   string `json:"image"`
	Status  string `json:"status"`
	LogPath string `json:"log_path,omitempty"`
}

// Result is the extracted judging result.
type Result struct {
	Score       float64         `json:"score"`
	Performance float64         `json:"performance"`
	Info        json.RawMessage `json:"info"`
	Status      string          `json:"status"`
}

// SubmitRequest carries the submission payload.
// CSOJ expects a multipart form with field name "files" and each file's
// Multipart filename set to base64(relative_path). The adapter encodes
// the paths transparently — callers provide normal relative paths.
type SubmitRequest struct {
	ProblemID string
	// Files maps normal relative paths to file content.
	// The adapter base64-encodes each key as the multipart filename.
	Files map[string][]byte
}

// ContestRecord represents CSOJ contest/problem projection.
// Fields after EndTime carry the real judge configuration from the platform.
type ContestRecord struct {
	ContestID string `json:"contest_id"`
	ProblemID string `json:"problem_id"`
	Title     string `json:"title"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	// Judge configuration — required by CSOJ for problem creation.
	Cluster  string `json:"cluster"`
	CPU      int    `json:"cpu"`
	Memory   int    `json:"memory"`
	Upload   map[string]interface{} `json:"upload"`
	Workflow []map[string]interface{} `json:"workflow"`
	Score    map[string]interface{} `json:"score"`
}

// --- API methods ---

// Submit sends files to CSOJ for judging.
// POST /api/v1/problems/:id/submit — multipart/form-data
// Each file's multipart filename is base64(relative_path) per
// vendor/csoj/internal/api/user/submission.go:162-168.
func (c *Client) Submit(ctx context.Context, req SubmitRequest) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	for relPath, content := range req.Files {
		encName := base64.StdEncoding.EncodeToString([]byte(relPath))
		part, err := w.CreateFormFile("files", encName)
		if err != nil {
			return "", fmt.Errorf("adapter: create form file %q: %w", relPath, err)
		}
		if _, err := part.Write(content); err != nil {
			return "", fmt.Errorf("adapter: write file %q: %w", relPath, err)
		}
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("adapter: close multipart: %w", err)
	}

	url := fmt.Sprintf("%s/problems/%s/submit", c.baseURL, req.ProblemID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return "", fmt.Errorf("adapter: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", w.FormDataContentType())
	httpReq.Header.Set("Authorization", "Bearer "+c.credential)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("adapter: submit %s: %w", req.ProblemID, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("adapter: read submit response: %w", err)
	}

	env, err := validateEnvelope(http.MethodPost, fmt.Sprintf("problems/%s/submit", req.ProblemID), resp, body)
	if err != nil {
		return "", err
	}

	var data struct {
		SubmissionID string `json:"submission_id"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return "", fmt.Errorf("adapter: parse submission_id: %w", err)
	}
	return data.SubmissionID, nil
}

// QueryResult reads submission status and result from CSOJ.
// GET /api/v1/submissions/:id
func (c *Client) QueryResult(ctx context.Context, submissionID string) (*Submission, error) {
	url := fmt.Sprintf("%s/submissions/%s", c.baseURL, submissionID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("adapter: new request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.credential)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("adapter: query %s: %w", submissionID, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("adapter: read query response: %w", err)
	}

	env, err := validateEnvelope(http.MethodGet, fmt.Sprintf("submissions/%s", submissionID), resp, body)
	if err != nil {
		return nil, err
	}

	var sub Submission
	if err := json.Unmarshal(env.Data, &sub); err != nil {
		return nil, fmt.Errorf("adapter: parse submission data: %w", err)
	}
	return &sub, nil
}

// StreamLogs opens a WebSocket connection to stream judge container logs.
// CSOJ's WS endpoint: /ws/submissions/:subID/containers/:conID/logs?token=<jwt>
// Emits NDJSON frames: {"stream":"stdout","data":"..."} or {"stream":"stderr","data":"..."}
// callback receives each parsed frame's stream name and data.
func (c *Client) StreamLogs(ctx context.Context, submissionID, containerID string, callback func(stream, data string) error) error {
	// Build WS URL from base HTTP URL
	wsURL := c.baseURL
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = fmt.Sprintf("%s/ws/submissions/%s/containers/%s/logs?token=%s",
		wsURL, url.PathEscape(submissionID), url.PathEscape(containerID), url.QueryEscape(c.credential))

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("adapter: websocket dial: %w", err)
	}
	defer conn.Close()

	// Close connection when context is cancelled
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("adapter: websocket read: %w", err)
		}
		var frame struct {
			Stream string `json:"stream"`
			Data   string `json:"data"`
		}
		if err := json.Unmarshal(message, &frame); err != nil {
			continue // skip non-NDJSON frames
		}
		if err := callback(frame.Stream, frame.Data); err != nil {
			return err
		}
	}
}

// SyncProblem ensures the platform problem has a corresponding CSOJ
// contest/problem record.
// Creates contest via POST /api/v1/contests then
// problem via POST /api/v1/contests/:contestID/problems.
// Routes from vendor/csoj/internal/api/admin/router.go:66,72.
func (c *Client) SyncProblem(ctx context.Context, rec ContestRecord) error {
	if err := c.upsertContest(ctx, rec); err != nil {
		return err
	}
	return c.upsertProblem(ctx, rec)
}

// doCSOJ sends a JSON request, reads the body, and validates the CSOJ envelope.
func (c *Client) doCSOJ(ctx context.Context, method, path string, body []byte) (*csjResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/"+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("adapter: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.credential)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("adapter: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("adapter: read %s %s: %w", method, path, err)
	}
	return validateEnvelope(method, path, resp, respBody)
}

// getResource returns (true, nil) if the resource exists, (false, nil) for HTTP 404
// (resource genuinely missing), and (false, error) for transport/5xx/malformed responses.
func (c *Client) getResource(ctx context.Context, path string) (bool, error) {
	_, err := c.doCSOJ(ctx, http.MethodGet, path, nil)
	if err != nil {
		var csjErr *CSOJError
		if errors.As(err, &csjErr) && csjErr.HTTPStatus == 404 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
func (c *Client) upsertContest(ctx context.Context, rec ContestRecord) error {
	payload := map[string]interface{}{"id": rec.ContestID, "name": rec.ContestID, "starttime": rec.StartTime, "endtime": rec.EndTime}
	body, _ := json.Marshal(payload)
	path := "contests/" + url.PathEscape(rec.ContestID)
	exists, err := c.getResource(ctx, path)
	if err != nil {
		return fmt.Errorf("adapter: check contest %s: %w", rec.ContestID, err)
	}
	if exists {
		_, err = c.doCSOJ(ctx, http.MethodPut, path, body)
	} else {
		_, err = c.doCSOJ(ctx, http.MethodPost, "contests", body)
	}
	return err
}

func (c *Client) upsertProblem(ctx context.Context, rec ContestRecord) error {
	// Build the CSOJ problem payload from platform config. If platform
	// config fields are empty, use safe defaults that won't overwrite
	// real judge definitions on a PUT.
	payload := map[string]interface{}{
		"id":        rec.ProblemID,
		"name":      rec.Title,
		"starttime": rec.StartTime,
		"endtime":   rec.EndTime,
	}
	// Only include judge config fields that were explicitly provided.
	if rec.Cluster != "" {
		payload["cluster"] = rec.Cluster
		payload["cpu"] = rec.CPU
		payload["memory"] = rec.Memory
		payload["upload"] = rec.Upload
		payload["workflow"] = rec.Workflow
		payload["score"] = rec.Score
	}
	body, _ := json.Marshal(payload)

	// Check if the problem is already under the target contest.
	// CSOJ stores problem IDs globally; a problem created under contest-A
	// will be found by GET /problems/:id even if it should be contest-B's.
	// Strategy: check the contest's problem list first, not the global lookup.
	contestPath := "contests/" + url.PathEscape(rec.ContestID)
	inContest := false
	contestEnv, err := c.doCSOJ(ctx, http.MethodGet, contestPath, nil)
	if err == nil && contestEnv != nil && contestEnv.Data != nil {
		var contestData struct {
			ID       string   `json:"id"`
			Problems []string `json:"problems"`
		}
		if err := json.Unmarshal(contestEnv.Data, &contestData); err == nil {
			for _, pid := range contestData.Problems {
				if pid == rec.ProblemID {
					inContest = true
					break
				}
			}
		}
	}
	// If contest not found (404), that's fine — proceed to create.

	if inContest {
		// Problem exists under this contest — safe to PUT full config.
		_, err = c.doCSOJ(ctx, http.MethodPut, "problems/"+url.PathEscape(rec.ProblemID), body)
		return err
	}

	// Check if problem exists globally (under a different contest).
	path := "problems/" + url.PathEscape(rec.ProblemID)
	exists, err := c.getResource(ctx, path)
	if err != nil {
		return fmt.Errorf("adapter: check problem %s: %w", rec.ProblemID, err)
	}
	if exists {
		// Problem exists but not in this contest — PUT updates definition only.
		_, err = c.doCSOJ(ctx, http.MethodPut, path, body)
	} else {
		// New problem — create under the target contest.
		_, err = c.doCSOJ(ctx, http.MethodPost, "contests/"+url.PathEscape(rec.ContestID)+"/problems", body)
	}
	return err
}

// SubmissionService is a thin wrapper that adapts adapter.Client to the
// controller.SubmissionService interface for use by the controller HTTP handler.
type SubmissionService struct {
	client *Client
}

// NewSubmissionService creates a controller-compatible submission service.
func NewSubmissionService(client *Client) *SubmissionService {
	return &SubmissionService{client: client}
}

// Submit implements the controller submission interface.
func (s *SubmissionService) Submit(ctx context.Context, problemID string, files map[string][]byte) (string, error) {
	return s.client.Submit(ctx, SubmitRequest{ProblemID: problemID, Files: files})
}

// QueryResult implements the controller submission interface by calling the CSOJ API.
func (s *SubmissionService) QueryResult(ctx context.Context, submissionID string) (*SubmissionResult, error) {
	sub, err := s.client.QueryResult(ctx, submissionID)
	if err != nil {
		return nil, err
	}
	info := ""
	if sub.Info != nil {
		info = string(sub.Info)
	}
	return &SubmissionResult{
		SubmissionID: sub.ID,
		ProblemID:    sub.ProblemID,
		Status:       sub.Status,
		Score:        float64(sub.Score),
		Performance:  float64(sub.Performance),
		Info:         info,
	}, nil
}

// SubmissionResult is the query result type for the controller interface.
type SubmissionResult struct {
	SubmissionID string  `json:"submission_id"`
	ProblemID    string  `json:"problem_id"`
	Status       string  `json:"status"`
	Score        float64 `json:"score"`
	Performance  float64 `json:"performance"`
	Info         string  `json:"info,omitempty"`
}
