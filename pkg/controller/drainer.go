package controller

// noopBastionDrainer is a BastionDrainer that performs no channel termination.
// It exists so the release call path for Draining is always exercised in tests
// and production, even before real bastion integration is available.
type NoopBastionDrainer struct{}

func (NoopBastionDrainer) Drain(principal string) error { return nil }
