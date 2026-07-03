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
type ContestRecord struct {
	ContestID string `json:"contest_id"`
	ProblemID string `json:"problem_id"`
	Title     string `json:"title"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
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

	var env csjResponse
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("adapter: parse submit response: %w (body: %s)", err, string(body))
	}
	if env.Code != 0 {
		return "", fmt.Errorf("adapter: CSOJ rejected submit (code=%d): %s", env.Code, env.Message)
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

	var env csjResponse
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("adapter: parse query response: %w", err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("adapter: CSOJ rejected query (code=%d): %s", env.Code, env.Message)
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

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
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
// contest/problem record. Uses CSOJ's admin API.
func (c *Client) SyncProblem(ctx context.Context, rec ContestRecord) error {
	return fmt.Errorf("adapter: SyncProblem not yet implemented")
}
