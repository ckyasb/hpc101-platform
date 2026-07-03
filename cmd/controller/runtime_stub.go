//go:build !go1.25

package main

import (
	"context"
	"fmt"

	"hpc101-platform/controller"
)

// Stub for go < 1.25: runtime requires go >= 1.25 (Docker SDK transitive dep).
// The production Docker image uses golang:1.25-alpine.
func newRuntimeAdapter(endpoint string) (controller.ContainerCreator, error) {
	return nil, fmt.Errorf("runtime requires go >= 1.25 (build with golang:1.25-alpine)")
}

type stubSubmission struct{}

func newSubmissionService() controller.SubmissionService {
	return &stubSubmission{}
}

func (s *stubSubmission) Submit(ctx context.Context, problemID string, files map[string][]byte) (string, error) {
	return "", fmt.Errorf("submission requires go >= 1.25")
}

func (s *stubSubmission) QueryResult(ctx context.Context, submissionID string) (*controller.SubmissionResult, error) {
	return nil, fmt.Errorf("submission requires go >= 1.25")
}
func newProblemSyncService() controller.ProblemSyncService {
	return nil
}
