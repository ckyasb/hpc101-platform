// podman-poc: DockerManager API compatibility harness for AC-1.
//
// Exercises the EXACT Docker API surface CSOJ's DockerManager uses
// (vendor/csoj/internal/judger/docker.go) against a Docker-compatible
// endpoint (Docker or Podman's docker-compat socket).
//
// Usage:
//
//	DOCKER_HOST=tcp://127.0.0.1:12375 go run .  # against local podman
//	DOCKER_HOST=unix:///run/podman/podman.sock go run .
//
// Tests: volume create/remove, container create+start with limits,
// tar copy, exec with multiplexed stdout/stderr, timeout cancellation,
// container cleanup.
package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

func main() {
	ctx := context.Background()

	dockerHost := os.Getenv("DOCKER_HOST")
	if dockerHost == "" {
		dockerHost = "unix:///run/podman/podman.sock"
	}
	fmt.Printf("=== hpc101-platform DockerManager API POC ===\n")
	fmt.Printf("DOCKER_HOST: %s\n\n", dockerHost)

	cli, err := client.NewClientWithOpts(client.WithHost(dockerHost), client.WithAPIVersionNegotiation())
	if err != nil {
		fatalf("NewClientWithOpts: %v", err)
	}
	defer cli.Close()

	// Pre-cleanup from previous runs
	for _, name := range []string{
		"hpc101-poc-vol", "hpc101-poc-ctr", "hpc101-poc-timeout",
	} {
		_ = cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
	}
	_ = cli.VolumeRemove(ctx, "hpc101-poc-vol", true)

	passed := 0
	failed := 0

	// Test 1: Volume Create/Remove
	testVol := "hpc101-poc-vol"
	fmt.Printf("--- Test 1: Volume Create ---\n")
	vol, err := cli.VolumeCreate(ctx, volume.CreateOptions{Name: testVol})
	if err != nil {
		failf("VolumeCreate: %v", err)
		failed++
	} else {
		fmt.Printf("  [PASS] Volume created: %s\n", vol.Name)
		passed++
	}

	// Test 2: Container Create with limits
	testContainer := "hpc101-poc-ctr"
	testImage := "docker.io/library/alpine:latest"
	fmt.Printf("\n--- Test 2: Container Create (with limits) ---\n")
	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image: testImage,
			Cmd:   []string{"sleep", "30"},
			Env:   []string{"TEST_ENV=hpc101"},
		},
		&container.HostConfig{
			Resources: container.Resources{
				NanoCPUs: 500_000_000,      // 0.5 CPU
				Memory:   64 * 1024 * 1024, // 64 MB
			},
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeVolume,
					Source: testVol,
					Target: "/mnt/work",
				},
			},
		},
		nil, nil, testContainer,
	)
	if err != nil {
		failf("ContainerCreate: %v", err)
		failed++
	} else {
		fmt.Printf("  [PASS] Container created: %s\n", resp.ID[:12])
		passed++
	}

	// Test 3: Container Start
	fmt.Printf("\n--- Test 3: Container Start ---\n")
	err = cli.ContainerStart(ctx, resp.ID, container.StartOptions{})
	if err != nil {
		failf("ContainerStart: %v", err)
		failed++
	} else {
		fmt.Printf("  [PASS] Container started\n")
		passed++
	}

	// Test 4: Copy To Container (tar archive)
	fmt.Printf("\n--- Test 4: Copy To Container ---\n")
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)
	tw.WriteHeader(&tar.Header{
		Name:     "test.txt",
		Size:     12,
		Mode:     0644,
		Typeflag: tar.TypeReg,
	})
	tw.Write([]byte("hello hpc101\n"))
	tw.Close()
	err = cli.CopyToContainer(ctx, resp.ID, "/mnt/work/", bytes.NewReader(tarBuf.Bytes()), container.CopyToContainerOptions{})
	if err != nil {
		failf("CopyToContainer: %v", err)
		failed++
	} else {
		fmt.Printf("  [PASS] Tar archive copied to /mnt/work/\n")
		passed++
	}

	// Test 5: Exec with stdout/stderr streaming (multiplexed)
	fmt.Printf("\n--- Test 5: Exec (multiplexed stdout/stderr) ---\n")
	execResp, err := cli.ContainerExecCreate(ctx, resp.ID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", "cat /mnt/work/test.txt && echo 'stderr line' >&2"},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		failf("ContainerExecCreate: %v", err)
		failed++
	} else {
		attachResp, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecStartOptions{})
		if err != nil {
			failf("ContainerExecAttach: %v", err)
			failed++
		} else {
			defer attachResp.Close()
			var stdoutBuf, stderrBuf bytes.Buffer
			_, err = stdcopy.StdCopy(&stdoutBuf, &stderrBuf, attachResp.Reader)
			if err != nil {
				failf("stdcopy.StdCopy: %v", err)
				failed++
			} else {
				stdoutStr := strings.TrimSpace(stdoutBuf.String())
				stderrStr := strings.TrimSpace(stderrBuf.String())
				if stdoutStr == "hello hpc101" && stderrStr == "stderr line" {
					fmt.Printf("  [PASS] stdout=%q stderr=%q\n", stdoutStr, stderrStr)
					passed++
				} else {
					failf("unexpected output: stdout=%q stderr=%q", stdoutStr, stderrStr)
					failed++
				}
			}
		}
	}

	// Test 6: Timeout cancellation (context.WithTimeout)
	fmt.Printf("\n--- Test 6: Timeout Cancellation ---\n")
	timeoutCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	shortCtr, err := cli.ContainerCreate(timeoutCtx,
		&container.Config{
			Image: testImage,
			Cmd:   []string{"sleep", "60"},
		},
		&container.HostConfig{},
		nil, nil, "hpc101-poc-timeout",
	)
	if err != nil {
		failf("ContainerCreate(timeout): %v", err)
		failed++
	} else {
		cli.ContainerStart(timeoutCtx, shortCtr.ID, container.StartOptions{})
		statusCh, errCh := cli.ContainerWait(timeoutCtx, shortCtr.ID, container.WaitConditionNotRunning)
		select {
		case <-statusCh:
			failf("ContainerWait completed before timeout (unexpected)")
			failed++
		case err := <-errCh:
			if err != nil {
				msg := err.Error()
				if strings.Contains(msg, "deadline") || strings.Contains(msg, "canceled") {
					fmt.Printf("  [PASS] Timeout correctly canceled after 2s: %s\n", msg)
					passed++
				} else {
					failf("ContainerWait error is not a timeout: %s", msg)
					failed++
				}
			} else {
				failf("ContainerWait nil error before timeout (unexpected)")
				failed++
			}
		}
		cli.ContainerRemove(context.Background(), shortCtr.ID, container.RemoveOptions{Force: true})
	}

	// Test 7: Container Stop + Remove (CleanupContainer)
	fmt.Printf("\n--- Test 7: Cleanup (stop + remove) ---\n")
	stopTimeout := 5
	err = cli.ContainerStop(ctx, resp.ID, container.StopOptions{Timeout: &stopTimeout})
	if err != nil {
		failf("ContainerStop: %v", err)
		failed++
	} else {
		fmt.Printf("  [PASS] Container stopped\n")
		passed++
	}
	err = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
	if err != nil {
		failf("ContainerRemove: %v", err)
		failed++
	} else {
		fmt.Printf("  [PASS] Container removed\n")
		passed++
	}

	// Test 8: Volume Remove
	fmt.Printf("\n--- Test 8: Volume Remove ---\n")
	err = cli.VolumeRemove(ctx, testVol, true)
	if err != nil {
		failf("VolumeRemove: %v", err)
		failed++
	} else {
		fmt.Printf("  [PASS] Volume removed\n")
		passed++
	}

	// Summary
	fmt.Printf("\n========================================\n")
	fmt.Printf("DockerManager API POC: %d passed, %d failed\n", passed, failed)
	if failed > 0 {
		fmt.Printf("AC-1 NOT SATISFIED: %d compatibility gaps found.\n", failed)
		os.Exit(1)
	}
	fmt.Printf("AC-1 COMPATIBLE: all %d Docker API operations succeed.\n", passed)
	fmt.Printf("Podman docker-compat endpoint matches CSOJ's DockerManager requirements.\n")
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}

func failf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "  [FAIL] "+format+"\n", args...)
}
