// Package e2e provides an environment-gated, CLI-driven end-to-end integration
// harness for hpc101-platform. It exercises the full course flow by building and
// invoking the real cmd/codojo binary, running a real ssh -J through the bastion,
// and asserting that no kubeconfig/OIDC path is required.
//
// The harness is gated behind the HPC101_E2E env var. When unset (the default in
// CI and unit-test runs), all tests are skipped. When set to "1", the tests require
// a live controller reachable at HPC101_CONTROLLER_URL and a real CSOJ + runtime +
// bastion stack.
//
// This harness proves AC-11: no kubeconfig, no OIDC, no manual intervention.
package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
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

func skipIfNotEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("HPC101_E2E") != "1" {
		t.Skip("HPC101_E2E not set; skipping live E2E test")
	}
}

func buildCLI(t *testing.T, dir string) string {
	t.Helper()
	binPath := filepath.Join(dir, "codojo")
	src := os.Getenv("HPC101_E2E_CLI_SRC")
	if src == "" {
		src = "../../cmd/codojo"
	}
	cmd := exec.Command("go", "build", "-o", binPath, src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	return binPath
}

func runCLI(t *testing.T, binPath, home, principal string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"USER="+principal,
		"CODOJO_CONTROLLER_URL="+controllerURL(),
		"KUBECONFIG=",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("codojo %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

func TestE2ECourseFlow(t *testing.T) {
	skipIfNotEnabled(t)

	home := t.TempDir()
	keyPath := filepath.Join(home, "student_key")
	if err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyPath, "-N", "", "-q").Run(); err != nil {
		t.Fatalf("ssh-keygen: %v", err)
	}
	binPath := buildCLI(t, home)

	// Unique, collision-proof principal to avoid colliding with real leases/keys.
	principal := fmt.Sprintf("e2e-%d-%d", time.Now().UnixNano(), os.Getpid())

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
		t.Fatal("HPC101_E2E_IMAGE not set; need a service image")
	}

	t.Run("register-key", func(t *testing.T) {
		out := runCLI(t, binPath, home, principal, "register-key", keyPath)
		t.Logf("register-key: %s", out)
	})

	t.Run("up", func(t *testing.T) {
		out := runCLI(t, binPath, home, principal, "up", image, course, problemID)
		t.Logf("up: %s", out)
	})

	// Register cleanup on the parent test (not the subtest) so it runs at
	// parent-test end and on failures after up. Best-effort release with the
	// same isolated env as runCLI so it releases the correct principal.
	t.Cleanup(func() {
		releaseCmd := exec.Command(binPath, "release")
		releaseCmd.Env = append(os.Environ(),
			"HOME="+home,
			"USER="+principal,
			"CODOJO_CONTROLLER_URL="+controllerURL(),
			"KUBECONFIG=",
		)
		// Ignore errors: the flow may have already released, or the controller
		// may be unreachable. This must not mask earlier test failures.
		_ = releaseCmd.Run()
	})

	var sshConfig string
	t.Run("ssh-info", func(t *testing.T) {
		sshConfig = runCLI(t, binPath, home, principal, "ssh-info")
		if !strings.Contains(sshConfig, "ProxyJump") {
			t.Fatalf("ssh-info output missing ProxyJump:\n%s", sshConfig)
		}
		t.Logf("ssh-info: received ProxyJump config")
	})

	t.Run("problem-sync", func(t *testing.T) {
		syncBody := fmt.Sprintf(`{"course":"%s","contest":"%s","problem_id":"%s","title":"E2E","start_time":"2026-01-01T00:00:00Z","end_time":"2027-01-01T00:00:00Z"}`, course, contest, problemID)
		out, err := exec.Command("curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
			"-X", "POST", controllerURL()+"/api/v1/problems/sync",
			"-H", "Content-Type: application/json",
			"-d", syncBody).Output()
		if err != nil {
			t.Fatalf("problem sync: %v", err)
		}
		code := strings.TrimSpace(string(out))
		if code != "201" && code != "200" {
			t.Fatalf("problem sync failed: HTTP %s", code)
		}
		t.Logf("problem sync: HTTP %s", code)
	})

	var submissionID string
	t.Run("submit", func(t *testing.T) {
		solutionPath := filepath.Join(home, "main.c")
		if err := os.WriteFile(solutionPath, []byte("int main(){return 0;}"), 0644); err != nil {
			t.Fatalf("write solution: %v", err)
		}
		out := runCLI(t, binPath, home, principal, "submit", course, contest, problemID, solutionPath)
		t.Logf("submit: %s", out)
		if idx := strings.Index(out, `"submission_id":"`); idx >= 0 {
			rest := out[idx+len(`"submission_id":"`):]
			if end := strings.Index(rest, `"`); end >= 0 {
				submissionID = rest[:end]
			}
		}
		if submissionID == "" {
			t.Fatalf("could not parse submission ID from: %s", out)
		}
		t.Logf("submission ID: %s", submissionID)
	})

	t.Run("score", func(t *testing.T) {
		if submissionID == "" {
			t.Skip("no submission to score")
		}
		out := runCLI(t, binPath, home, principal, "score", submissionID)
		t.Logf("score: %s", out)
		if !strings.Contains(out, "score:") {
			t.Fatalf("score output missing result:\n%s", out)
		}
	})

	t.Run("logs", func(t *testing.T) {
		if submissionID == "" {
			t.Skip("no submission to fetch logs for")
		}
		out := runCLI(t, binPath, home, principal, "logs", submissionID)
		t.Logf("logs: %s (length %d)", out, len(out))
		// Logs must be non-empty and must not be a JSON error body.
		if len(out) == 0 {
			t.Fatal("logs output is empty; expected log lines")
		}
		if strings.HasPrefix(strings.TrimSpace(out), "{") && strings.Contains(out, "error") {
			t.Fatalf("logs returned a JSON error instead of log lines: %s", out)
		}
	})

	t.Run("ssh-j", func(t *testing.T) {
		configPath := filepath.Join(home, "ssh_config")
		if err := os.WriteFile(configPath, []byte(sshConfig), 0600); err != nil {
			t.Fatalf("write ssh config: %v", err)
		}
		cmd := exec.Command("ssh", "-F", configPath,
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "ConnectTimeout=10",
			"hpc101-container", "echo", "ssh-j-ok")
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("ssh -J: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "ssh-j-ok") {
			t.Fatalf("ssh -J output unexpected: %s", out)
		}
		t.Logf("ssh -J: connected and ran command successfully")
	})

	t.Run("release", func(t *testing.T) {
		out := runCLI(t, binPath, home, principal, "release")
		t.Logf("release: %s", out)
	})
}

func TestE2ENoKubeconfig(t *testing.T) {
	skipIfNotEnabled(t)
	out, err := exec.Command("curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
		controllerURL()+"/healthz").Output()
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if strings.TrimSpace(string(out)) != "200" {
		t.Fatalf("healthz: expected 200, got %s (no kubeconfig/OIDC should be required)", out)
	}
}
