package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func getControllerURL() string {
	if u := os.Getenv("CODOJO_CONTROLLER_URL"); u != "" {
		return u
	}
	return "http://controller.hpc101-platform.svc.cluster.local:8080"
}

type config struct {
	SSHPublicKey   string `json:"ssh_public_key"`
	PrivateKeyPath string `json:"private_key_path"`
}

func loadConfig() *config {
	p := filepath.Join(os.Getenv("HOME"), ".hpc101", "config.json")
	d, err := os.ReadFile(p)
	if err != nil {
		return &config{}
	}
	var c config
	if err := json.Unmarshal(d, &c); err != nil {
		return &config{}
	}
	return &c
}

type serviceReq struct {
	Principal string `json:"principal"`
	Image     string `json:"image"`
	SSHKey    string `json:"ssh_key"`
	Course    string `json:"course"`
	Problem   string `json:"problem"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "register-key":
		registerKey(os.Args[2:])
	case "up":
		up(os.Args[2:])
	case "ssh-info":
		sshInfo()
	case "release":
		release()
	case "problem":
		listProblems()
	case "score":
		showScores()
	case "submit":
		submit(os.Args[2:])
	case "logs":
		fetchLogs(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "codojo: hpc101-platform CLI (no kubeconfig)")
	for _, s := range []string{
		"  register-key <path>",
		"  up <image> [course] [problem]",
		"  ssh-info", "  release",
		"  problem", "  score",
		"  submit <course> <contest> <problem-id> <file>...",
		"  logs <submission-id>",
		"  score [submission-id]",
	} {
		fmt.Fprintln(os.Stderr, s)
	}
}

func registerKey(args []string) {
	if len(args) < 1 {
		fatal("usage: register-key <private-key-path>")
	}
	privPath := args[0]
	var pubPath string
	if strings.HasSuffix(privPath, ".pub") {
		pubPath = privPath
		privPath = strings.TrimSuffix(privPath, ".pub")
	} else {
		pubPath = privPath + ".pub"
	}
	d, err := os.ReadFile(pubPath)
	if err != nil {
		fatal("read public key %s: %v", pubPath, err)
	}
	key := strings.TrimSpace(string(d))
	principal := os.Getenv("USER")

	// Register with the controller
	payload, _ := json.Marshal(map[string]string{"principal": principal, "public_key": key})
	resp, err := http.Post(getControllerURL()+"/api/v1/keys", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		fatal("register key: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		fatal("register key failed: %s", b)
	}

	// Also save locally for reference
	dir := filepath.Join(os.Getenv("HOME"), ".hpc101")
	if err := os.MkdirAll(dir, 0700); err != nil {
		fatal("mkdir: %v", err)
	}
	cfgB, _ := json.MarshalIndent(config{SSHPublicKey: key, PrivateKeyPath: privPath}, "", "  ")
	os.WriteFile(filepath.Join(dir, "config.json"), cfgB, 0600)
	fmt.Printf("key registered with controller (identity: %s)\n", privPath)
}

func up(args []string) {
	if len(args) < 1 {
		fatal("usage: up <image> [course] [problem]")
	}
	req := serviceReq{
		Principal: os.Getenv("USER"),
		Image:     args[0],
		SSHKey:    loadConfig().SSHPublicKey,
		Course:    "default",
		Problem:   "default",
	}
	if len(args) >= 2 {
		req.Course = args[1]
	}
	if len(args) >= 3 {
		req.Problem = args[2]
	}
	body, err := json.Marshal(req)
	if err != nil {
		fatal("marshal: %v", err)
	}
	resp, err := http.Post(getControllerURL()+"/api/v1/services", "application/json", strings.NewReader(string(body)))
	if err != nil {
		fatal("POST services: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var r map[string]interface{}
	json.Unmarshal(b, &r)
	if resp.StatusCode == 201 {
		fmt.Printf("ready: %s:%v\n", r["host"], r["port"])

		// Save the signed certificate if returned
		if cert, ok := r["certificate"].(string); ok && cert != "" {
			dir := filepath.Join(os.Getenv("HOME"), ".hpc101")
			os.MkdirAll(dir, 0700)
			certPath := filepath.Join(dir, req.Principal+"-key-cert.pub")
			if err := os.WriteFile(certPath, []byte(cert), 0600); err != nil {
				fmt.Fprintf(os.Stderr, "warning: save cert: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "cert saved: %s\n", certPath)
			}
		}
		if warn, ok := r["cert_warning"].(string); ok {
			fmt.Fprintf(os.Stderr, "warning: %s\n", warn)
		}
	} else {
		fatal("error: %s", b)
	}
}

func sshInfo() {
	cfg := loadConfig()
	p := url.QueryEscape(os.Getenv("USER"))
	resp, err := http.Get(getControllerURL() + "/api/v1/ssh-info?principal=" + p)
	if err != nil {
		fatal("GET ssh-info: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var r map[string]interface{}
	json.Unmarshal(b, &r)
	if resp.StatusCode == 200 {
		if sshCfg, ok := r["ssh_config"].(string); ok {
			if cfg.PrivateKeyPath != "" {
				sshCfg = strings.Replace(sshCfg,
					"IdentityFile ~/.hpc101/"+p+"-key",
					"IdentityFile "+cfg.PrivateKeyPath, 1)
			}
			fmt.Print(sshCfg)
		} else {
			fmt.Printf("bastion: %s:%v  container: %s:%v  config_dir: %s\n",
				r["bastion_host"], r["bastion_port"], r["container_host"], r["container_port"], r["config_dir"])
		}
	} else {
		fatal("no active environment")
	}
}

func release() {
	p := url.QueryEscape(os.Getenv("USER"))
	req, _ := http.NewRequest(http.MethodDelete, getControllerURL()+"/api/v1/release?principal="+p, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("DELETE release: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var r map[string]string
	json.Unmarshal(b, &r)
	if resp.StatusCode == 200 {
		fmt.Printf("released: %s\n", r["status"])
	} else {
		fatal("error: %s", b)
	}
}

func listProblems() {
	resp, err := http.Get(getControllerURL() + "/api/v1/problems")
	if err != nil {
		fatal("GET problems: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		fatal("read: %v", err)
	}
	var r map[string]interface{}
	json.Unmarshal(b, &r)
	problems, _ := r["problems"].([]interface{})
	if len(problems) == 0 {
		fmt.Println("no problems")
		return
	}
	for _, p := range problems {
		fmt.Printf("  %v\n", p)
	}
}

func showScores() {
	// If a submission ID is provided, poll until terminal and print result.
	if len(os.Args) > 2 {
		pollScore(os.Args[2])
		return
	}
	resp, err := http.Get(getControllerURL() + "/api/v1/scores")
	if err != nil {
		fatal("GET scores: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		fatal("read: %v", err)
	}
	var r map[string]interface{}
	json.Unmarshal(b, &r)
	scores, _ := r["scores"].([]interface{})
	if len(scores) == 0 {
		fmt.Println("no scores")
		return
	}
	for _, s := range scores {
		fmt.Printf("  %v\n", s)
	}
}

func pollScore(submissionID string) {
	url := getControllerURL() + "/api/v1/submissions/" + submissionID
	for i := 0; i < 60; i++ {
		resp, err := http.Get(url)
		if err != nil {
			fatal("GET submission: %v", err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var r map[string]interface{}
		json.Unmarshal(b, &r)
		status, _ := r["status"].(string)
		if status == "Success" || status == "Failed" {
			fmt.Printf("status: %s\nscore: %v\nperformance: %v\ninfo: %v\n",
				status, r["score"], r["performance"], r["info"])
			return
		}
		if status == "Queued" || status == "Running" {
			fmt.Printf("\r%s...", status)
			time.Sleep(2 * time.Second)
			continue
		}
		fmt.Printf("unknown status: %s\n%s\n", status, b)
		return
	}
	fatal("timeout waiting for submission result")
}

func fetchLogs(args []string) {
	if len(args) < 1 {
		fatal("usage: logs <submission-id>")
	}
	url := getControllerURL() + "/api/v1/submissions/logs/" + args[0]
	resp, err := http.Get(url)
	if err != nil {
		fatal("GET logs: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fatal("read logs: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fatal("logs: HTTP %d: %s", resp.StatusCode, body)
	}
	os.Stdout.Write(body)
}

type submitReq struct {
	ProblemID string            `json:"problem_id"`
	Files     map[string]string `json:"files"`
}

func submit(args []string) {
	if len(args) < 4 {
		fatal("usage: submit <course> <contest> <problem-id> <file>...")
	}
	course := args[0]
	contest := args[1]
	problemID := args[2]
	req := submitReq{ProblemID: problemID, Files: map[string]string{}}
	for _, fp := range args[3:] {
		data, err := os.ReadFile(fp)
		if err != nil {
			fatal("read %s: %v", fp, err)
		}
		req.Files[fp] = base64.StdEncoding.EncodeToString(data)
	}
	body, err := json.Marshal(req)
	if err != nil {
		fatal("marshal: %v", err)
	}
	resp, err := http.Post(getControllerURL()+"/api/v1/submissions?course="+url.QueryEscape(course)+"&contest="+url.QueryEscape(contest), "application/json", strings.NewReader(string(body)))
	if err != nil {
		fatal("POST submit: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		fatal("read response: %v", err)
	}
	if resp.StatusCode >= 300 {
		fatal("submit failed: %s", b)
	}
	fmt.Printf("submitted: %s\n", b)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
