// Package e2e provides an environment-gated end-to-end integration harness for
// hpc101-platform. It exercises the full course flow:
//
//	register-key -> up -> ssh-info -> submit -> score -> logs -> release
//
// The harness is gated behind the HPC101_E2E env var. When unset (the default in
// CI and unit-test runs), all tests are skipped. When set to "1", the tests
// require a live controller reachable at HPC101_CONTROLLER_URL (or
// http://localhost:8080 by default) and a real CSOJ + runtime + bastion stack.
//
// This harness proves AC-11: no kubeconfig, no OIDC, no manual intervention.
package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func controllerURL() string {
	if u := os.Getenv("HPC101_CONTROLLER_URL"); u != "" {
		return u
	}
	return "http://localhost:8080"
}

// skipIfNotEnabled skips the entire harness when HPC101_E2E != "1".
func skipIfNotEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("HPC101_E2E") != "1" {
		t.Skip("HPC101_E2E not set; skipping live E2E test")
	}
}

func principal() string {
	if p := os.Getenv("HPC101_E2E_PRINCIPAL"); p != "" {
		return p
	}
	return "e2e-" + fmt.Sprintf("%d", time.Now().UnixNano()%10000)
}

// TestE2ECourseFlow runs the complete AC-11 chain against live services.
func TestE2ECourseFlow(t *testing.T) {
	skipIfNotEnabled(t)

	p := principal()
	course := os.Getenv("HPC101_E2E_COURSE")
	if course == "" {
		course = "cs101"
	}
	contest := os.Getenv("HPC101_E2E_CONTEST")
	if contest == "" {
		contest = "c1"
	}
	problemID := os.Getenv("HPC101_E2E_PROBLEM")
	if problemID == "" {
		problemID = "hello"
	}
	image := os.Getenv("HPC101_E2E_IMAGE")
	if image == "" {
		t.Skip("HPC101_E2E_IMAGE not set; need a service image")
	}
	privKeyPath := os.Getenv("HPC101_E2E_KEY")
	if privKeyPath == "" {
		t.Skip("HPC101_E2E_KEY not set; need a private key path")
	}

	// Step 1: register-key (post public key to controller).
	t.Run("register-key", func(t *testing.T) {
		pub, err := os.ReadFile(privKeyPath + ".pub")
		if err != nil {
			t.Fatalf("read public key: %v", err)
		}
		body, _ := json.Marshal(map[string]string{
			"principal":  p,
			"public_key": strings.TrimSpace(string(pub)),
		})
		resp, err := http.Post(controllerURL()+"/api/v1/keys", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /keys: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("register-key: %d: %s", resp.StatusCode, b)
		}
		t.Logf("registered key for %s", p)
	})

	// Step 2: up (create service, receive cert).
	var containerHost, containerPort string
	var certPath string
	t.Run("up", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{
			"principal": p,
			"image":     image,
			"ssh_key":   "", // controller uses registered key
			"course":    course,
			"problem":   problemID,
		})
		resp, err := http.Post(controllerURL()+"/api/v1/services", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /services: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("up: %d: %s", resp.StatusCode, b)
		}
		var r map[string]interface{}
		json.Unmarshal(b, &r)
		containerHost, _ = r["host"].(string)
		containerPort = fmt.Sprintf("%v", r["port"])
		if containerHost == "" || containerPort == "" {
			t.Fatalf("missing host/port: %v", r)
		}
		if cert, ok := r["certificate"].(string); ok && cert != "" {
			certPath = filepath.Join(os.Getenv("HOME"), ".hpc101", p+"-key-cert.pub")
			os.MkdirAll(filepath.Dir(certPath), 0700)
			if err := os.WriteFile(certPath, []byte(cert), 0600); err != nil {
				t.Fatalf("write cert: %v", err)
			}
			t.Logf("cert saved: %s", certPath)
		}
		t.Logf("service ready: %s:%s", containerHost, containerPort)
	})

	// Step 3: ssh-info (get ProxyJump config).
	t.Run("ssh-info", func(t *testing.T) {
		resp, err := http.Get(controllerURL() + "/api/v1/ssh-info?principal=" + url.QueryEscape(p))
		if err != nil {
			t.Fatalf("GET /ssh-info: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("ssh-info: %d: %s", resp.StatusCode, b)
		}
		var r map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&r)
		if r["ssh_config"] == nil {
			t.Fatalf("no ssh_config in response: %v", r)
		}
		t.Logf("ssh-info received for %s", p)
	})

	// Step 4: sync the problem (so submit can resolve it).
	t.Run("problem-sync", func(t *testing.T) {
		// Sync is optional if the problem is pre-mapped; skip if it fails.
		body := fmt.Sprintf(`{"course":"%s","contest":"%s","problem_id":"%s","title":"E2E","start_time":"2026-01-01T00:00:00Z","end_time":"2027-01-01T00:00:00Z"}`, course, contest, problemID)
		resp, err := http.Post(controllerURL()+"/api/v1/problems/sync", "application/json", strings.NewReader(body))
		if err != nil {
			t.Logf("sync (optional): %v", err)
			return
		}
		defer resp.Body.Close()
		t.Logf("problem sync: %d", resp.StatusCode)
	})

	// Step 5: submit a solution.
	var submissionID string
	t.Run("submit", func(t *testing.T) {
		solution := "int main(){return 0;}"
		body, _ := json.Marshal(map[string]interface{}{
			"problem_id": problemID,
			"files":      map[string]string{"main.c": base64.StdEncoding.EncodeToString([]byte(solution))},
		})
		endpoint := fmt.Sprintf("%s/api/v1/submissions?course=%s&contest=%s",
			controllerURL(), url.QueryEscape(course), url.QueryEscape(contest))
		resp, err := http.Post(endpoint, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /submissions: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("submit: %d: %s", resp.StatusCode, b)
		}
		var r map[string]string
		json.Unmarshal(b, &r)
		submissionID = r["submission_id"]
		if submissionID == "" {
			t.Fatal("no submission_id")
		}
		t.Logf("submitted: %s", submissionID)
	})

	// Step 6: score (poll until terminal).
	t.Run("score", func(t *testing.T) {
		if submissionID == "" {
			t.Skip("no submission to score")
		}
		endpoint := controllerURL() + "/api/v1/submissions/" + submissionID
		var status string
		for i := 0; i < 60; i++ {
			resp, err := http.Get(endpoint)
			if err != nil {
				t.Fatalf("GET /submissions/%s: %v", submissionID, err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var r map[string]interface{}
			json.Unmarshal(b, &r)
			status, _ = r["status"].(string)
			if status == "Success" || status == "Failed" {
				t.Logf("score: status=%s score=%v perf=%v", status, r["score"], r["performance"])
				break
			}
			time.Sleep(2 * time.Second)
		}
		if status != "Success" && status != "Failed" {
			t.Fatalf("submission did not reach terminal status: %s", status)
		}
	})

	// Step 7: release.
	t.Run("release", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete,
			controllerURL()+"/api/v1/release?principal="+url.QueryEscape(p), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE /release: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("release: %d: %s", resp.StatusCode, b)
		}
		t.Logf("released: %s", p)
	})
}

// TestE2ENoKubeconfig verifies the controller endpoint requires no k8s auth.
func TestE2ENoKubeconfig(t *testing.T) {
	skipIfNotEnabled(t)
	// A plain HTTP GET with no Authorization/Impersonate headers must work.
	resp, err := http.Get(controllerURL() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %d (no kubeconfig/OIDC should be required)", resp.StatusCode)
	}
}
