package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"hpc101-platform/controller"
	"hpc101-platform/sshca"
)

func main() {
	endpoint := os.Getenv("HPC101_RUNTIME_ENDPOINT")
	if endpoint == "" {
		log.Fatal("HPC101_RUNTIME_ENDPOINT required (e.g. tcp://podman-runtime.hpc101-runtime:2375)")
	}
	rt, err := newRuntimeAdapter(endpoint)
	if err != nil {
		log.Fatalf("controller: runtime adapter: %v", err)
	}
	sub := newSubmissionService()
	store := controller.NewSerializedStore()
	if disc, ok := interface{}(rt).(controller.DiscoveryClient); ok {
		if _, err := controller.ReattachLeases(store, disc); err != nil {
			log.Printf("reattach: %v", err)
		}
	} else {
		log.Printf("reattach: runtime does not support discovery (use go1.25 build)")
	}
	var drainer controller.BastionDrainer
	mgmtURL := os.Getenv("HPC101_BASTION_MGMT_URL")
	if mgmtURL != "" {
		drainer = controller.NewHTTPBastionDrainer(mgmtURL)
		log.Printf("controller: bastion drainer wired to %s", mgmtURL)
	} else if os.Getenv("HPC101_DEV_MODE") == "1" {
		drainer = &controller.NoopBastionDrainer{}
		log.Printf("controller: HPC101_DEV_MODE=1, using noop drainer")
	} else {
		log.Fatal("HPC101_BASTION_MGMT_URL is required in production; set HPC101_DEV_MODE=1 for local development")
	}

	// Load or generate the SSH CA for signing student certificates.
	var h *controller.Handler
	caPath := os.Getenv("HPC101_CA_KEY_PATH")
	if caPath == "" {
		caPath = "ca_key"
	}
	var certSigner controller.CertSigner
	ca, caErr := loadOrGenerateCA(caPath)
	if caErr != nil {
		log.Printf("controller: SSH CA: %v — cert signing disabled", caErr)
	} else {
		certSigner = &caAdapter{ca: ca}
		log.Printf("controller: SSH CA loaded, cert signing enabled")
	}

	// Wire problem sync service.
	problemSync := newProblemSyncService()
	if problemSync == nil {
		log.Printf("controller: problem sync not available (go < 1.25)")
	} else {
		log.Printf("controller: problem sync enabled")
	}
	h = controller.NewHandlerWithOpts(store, rt, sub, controller.HandlerOpts{
		Drainer:     drainer,
		CertSigner:  certSigner,
		ProblemSync: problemSync,
	})

	interval := 30 * time.Second
	if v := os.Getenv("HPC101_RELEASE_TRIGGER_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}
	controller.StartReleaseTriggers(context.Background(), store, rt, drainer, interval)

	log.Println("controller listening on :8080")
	if err := http.ListenAndServe(":8080", h); err != nil {
		log.Fatalf("controller: %v", err)
	}
}

// caAdapter implements controller.CertSigner using the sshca package.
type caAdapter struct {
	ca *sshca.CA
}

func (a *caAdapter) SignUserCert(pubKeyStr, principal string, validHours int) (string, error) {
	return a.ca.SignUserCertFromStrings(pubKeyStr, principal, validHours)
}

// loadOrGenerateCA loads an existing CA key file or generates a new one.
func loadOrGenerateCA(path string) (*sshca.CA, error) {
	ca, err := sshca.LoadCA(path)
	if err == nil {
		return ca, nil
	}
	// Generate a new CA if not found
	ca, genErr := sshca.GenerateCA()
	if genErr != nil {
		return nil, genErr
	}
	if saveErr := ca.SavePrivateKey(path); saveErr != nil {
		log.Printf("controller: save CA key: %v", saveErr)
	}
	return ca, nil
}
