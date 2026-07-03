//go:build go1.25

package main

import (
	"context"
	"os"

	"hpc101-platform/adapter"
	"hpc101-platform/controller"
)

// adapterSubmission wraps adapter.SubmissionService to satisfy
// controller.SubmissionService. It bridges the type gap between the
// adapter's SubmissionResult and controller's SubmissionResult.
type adapterSubmission struct {
	svc *adapter.SubmissionService
}

func newSubmissionService() controller.SubmissionService {
	baseURL := os.Getenv("CSOJ_API_URL")
	if baseURL == "" {
		baseURL = "http://csoj.hpc101-platform.svc.cluster.local:8080/api/v1"
	}
	token := os.Getenv("CSOJ_JWT_TOKEN")
	c := adapter.NewClient(baseURL, token)
	return &adapterSubmission{svc: adapter.NewSubmissionService(c)}
}

func (s *adapterSubmission) Submit(ctx context.Context, problemID string, files map[string][]byte) (string, error) {
	return s.svc.Submit(ctx, problemID, files)
}

func (s *adapterSubmission) QueryResult(ctx context.Context, submissionID string) (*controller.SubmissionResult, error) {
	r, err := s.svc.QueryResult(ctx, submissionID)
	if err != nil {
		return nil, err
	}
	containers := make([]controller.ContainerInfo, len(r.Containers))
	for i, c := range r.Containers {
		containers[i] = controller.ContainerInfo{ID: c.ID, Image: c.Image}
	}
	return &controller.SubmissionResult{
		SubmissionID: r.SubmissionID,
		ProblemID:    r.ProblemID,
		Status:       r.Status,
		Score:        r.Score,
		Performance:  r.Performance,
		Info:         r.Info,
		Containers:   containers,
	}, nil
}

func (s *adapterSubmission) StreamLogs(ctx context.Context, submissionID, containerID string, cb func(stream, data string) error) error {
	return s.svc.StreamLogs(ctx, submissionID, containerID, cb)
}
