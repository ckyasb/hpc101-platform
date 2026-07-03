package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const controllerURL = "http://controller.hpc101-platform.svc.cluster.local:8080"

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
		sshInfo(os.Args[2:])
	case "release":
		fmt.Println("release: not yet implemented")
	case "problem":
		fmt.Println("problem: not yet implemented")
	case "score":
		fmt.Println("score: not yet implemented")
	case "submit":
		fmt.Println("submit: not yet implemented")
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "codojo: hpc101-platform CLI (no kubeconfig)")
	fmt.Fprintln(os.Stderr, "  register-key <path>")
	fmt.Fprintln(os.Stderr, "  up <image> [course] [problem]")
	fmt.Fprintln(os.Stderr, "  ssh-info")
	fmt.Fprintln(os.Stderr, "  release")
	fmt.Fprintln(os.Stderr, "  problem / score / submit")
}

func registerKey(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: register-key <path-to-public-key>")
		os.Exit(1)
	}
	keyData, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading key: %v\n", err)
		os.Exit(1)
	}
	key := strings.TrimSpace(string(keyData))
	configDir := filepath.Join(os.Getenv("HOME"), ".codojo")
	os.MkdirAll(configDir, 0700)
	cfg := map[string]string{"ssh_public_key": key}
	cfgData, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(filepath.Join(configDir, "config.json"), cfgData, 0600)
	fmt.Println("key registered")
}

func up(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: up <image> [course] [problem]")
		os.Exit(1)
	}
	image := args[0]
	course, problem := "default", "default"
	if len(args) >= 2 {
		course = args[1]
	}
	if len(args) >= 3 {
		problem = args[2]
	}
	principal := os.Getenv("USER")
	cfgPath := filepath.Join(os.Getenv("HOME"), ".codojo", "config.json")
	cfgData, _ := os.ReadFile(cfgPath)
	var cfg map[string]string
	json.Unmarshal(cfgData, &cfg)
	body := fmt.Sprintf(`{"principal":"%s","image":"%s","ssh_key":"%s","course":"%s","problem":"%s"}`,
		principal, image, cfg["ssh_public_key"], course, problem)
	resp, err := http.Post(controllerURL+"/api/v1/services", "application/json", strings.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var r map[string]interface{}
	json.Unmarshal(respBody, &r)
	if resp.StatusCode == 201 {
		fmt.Printf("ready: %s:%v\n", r["host"], r["port"])
	} else {
		fmt.Fprintf(os.Stderr, "error: %s\n", respBody)
		os.Exit(1)
	}
}

func sshInfo(args []string) {
	principal := os.Getenv("USER")
	u := fmt.Sprintf("%s/api/v1/leases?principal=%s", controllerURL, principal)
	resp, err := http.Get(u)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var r map[string]interface{}
	json.Unmarshal(respBody, &r)
	if resp.StatusCode == 200 {
		fmt.Printf("bastion: %s port %v\n  ssh -J bastion user@%s\n", r["container_host"], r["container_port"], r["container_host"])
	} else {
		fmt.Fprintln(os.Stderr, "no active environment")
		os.Exit(1)
	}
}
