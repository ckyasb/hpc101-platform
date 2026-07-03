package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func getControllerURL() string {
	if u := os.Getenv("CODOJO_CONTROLLER_URL"); u != "" {
		return u
	}
	return "http://controller.hpc101-platform.svc.cluster.local:8080"
}

type config struct {
	SSHPublicKey string `json:"ssh_public_key"`
}

func loadConfig() *config {
	p := filepath.Join(os.Getenv("HOME"), ".codojo", "config.json")
	d, err := os.ReadFile(p)
	if err != nil {
		return &config{}
	}
	var c config
	json.Unmarshal(d, &c)
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
		"  submit <problem-id> <file>...",
	} {
		fmt.Fprintln(os.Stderr, s)
	}
}

func registerKey(args []string) {
	if len(args) < 1 {
		fatal("usage: register-key <path>")
	}
	d, err := os.ReadFile(args[0])
	if err != nil {
		fatal("read key: %v", err)
	}
	key := strings.TrimSpace(string(d))
	dir := filepath.Join(os.Getenv("HOME"), ".codojo")
	os.MkdirAll(dir, 0700)
	b, _ := json.MarshalIndent(config{SSHPublicKey: key}, "", "  ")
	os.WriteFile(filepath.Join(dir, "config.json"), b, 0600)
	fmt.Println("key registered")
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
	body, _ := json.Marshal(req)
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
	} else {
		fatal("error: %s", b)
	}
}

func sshInfo() {
	p := url.QueryEscape(os.Getenv("USER"))
	resp, err := http.Get(getControllerURL() + "/api/v1/leases?principal=" + p)
	if err != nil {
		fatal("GET leases: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var r map[string]interface{}
	json.Unmarshal(b, &r)
	if resp.StatusCode == 200 {
		fmt.Printf("bastion: %s:%v\n", r["container_host"], r["container_port"])
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
	fmt.Println("no problems configured")
}

func showScores() {
	p := url.QueryEscape(os.Getenv("USER"))
	resp, err := http.Get(getControllerURL() + "/api/v1/leases?principal=" + p)
	if err != nil {
		fatal("GET leases: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Println("no scores")
		return
	}
	fmt.Println("scores unavailable (CSOJ adapter pending)")
}

func submit(args []string) {
	if len(args) < 2 {
		fatal("usage: submit <problem-id> <file>...")
	}
	fmt.Printf("submitting %v to problem %s\n", args[1:], args[0])
	fmt.Println("submit pending CSOJ adapter integration")
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
