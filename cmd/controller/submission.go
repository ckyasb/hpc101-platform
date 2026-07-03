//go:build go1.25

package main

import (
	"context"
	"os"

	"hpc101-platform/adapter"
	"hpc101-platform/controller"
)

type adapterSubmission struct {
	client *adapter.Client
}

func newSubmissionService() controller.SubmissionService {
	baseURL := os.Getenv("CSOJ_API_URL")
	if baseURL == "" {
		baseURL = "http://csoj.hpc101-platform.svc.cluster.local:8080/api/v1"
	}
	token := os.Getenv("CSOJ_JWT_TOKEN")
	c := adapter.NewClient(baseURL, token)
	return &adapterSubmission{client: c}
}

func (s *adapterSubmission) Submit(ctx context.Context, problemID string, files map[string][]byte) (string, error) {
	return s.client.Submit(ctx, adapter.SubmitRequest{
		ProblemID: problemID,
		Files:     files,
	})
}
