# CSOJ ↔ Podman Docker API Contract

This documents the EXACT docker API surface CSOJ uses through its `DockerManager`
at `vendor/csoj/internal/judger/docker.go`. CSOJ code is zero-change; this is the
integration seam the hpc101-platform podman runtime must satisfy.

## CSOJ's DockerManager (Go SDK: github.com/docker/docker/client)

| Method | Docker API | Used In | Verification Strategy |
|--------|-----------|---------|-----------------------|
| `NewDockerManager(cfg DockerConfig)` | `client.WithHost(cfg.Host)` | `dispatcher.go:Dispatch` | Build-time verified: CSOJ binary builds. Config parse verified: CSOJ loads our `config.yaml` with `host: tcp://dedicated-judge-runtime...:2375`. |
| `CreateVolume(ctx, name)` | `POST /volumes/create` | `dispatcher.go:Dispatch` (per submission) | Create a named volume, then remove after judging. Verify: `podman volume create; podman volume rm` |
| `CreateContainer(ctx, image, volName, cpu, cpusetcpus, mem, asRoot, mounts, net, name, envs)` | `POST /containers/create` | `dispatcher.go:runWorkflowStep` | Create with resource limits (NanoCPUs/CpusetCpus/Memory), User mapping (1000:1000 when !asRoot), volume mount at `/mnt/work`, custom mounts (bind/tmpfs/volume, ReadOnly), NetworkDisabled toggle, env injection. Verify: `podman create --cpus=... --memory=... -v vol:/mnt/work ...` |
| `ExecInContainer(ctx, cid, cmd, callback)` | `POST /containers/{id}/exec` | `dispatcher.go:runWorkflowStep` | Execute in running container, stream stdout/stderr via `stdcopy.StdCopy`. Verify: `podman exec <cid> <cmd>` |
| `CopyToContainer(ctx, cid, tarPath, content)` | `PUT /containers/{id}/archive` | `dispatcher.go:runWorkflowStep` | Copy tar archive into container (for submission files on step 0). Verify: `podman cp <file> <cid>:/path` |
| `CleanupContainer(ctx, cid)` | `POST /containers/{id}/stop` + `DELETE /containers/{id}` | `dispatcher.go:Dispatch` (deferred) | Force stop then remove. Verify: `podman stop; podman rm -f` |
| `client.ContainerWait(ctx, cid, condition)` | `POST /containers/{id}/wait` | `dispatcher.go:runWorkflowStep` | Wait for container exit or timeout (context.WithTimeout). Verify: `podman wait <cid>` |

## Required Podman Compatibility

The podman runtime endpoint (`tcp://dedicated-judge-runtime.hpc101-runtime.svc.cluster.local:2375`)
MUST implement the above Docker API subset. Podman's `podman system service` with
docker-compat mode (`--time=0 tcp://0.0.0.0:2375`) exposes this API.

Known compat gaps to test in the runtime POC (blocked on podman deploy):
1. `CpusetCpus` (CSOJ `dispatcher.go` pinning) — may not work under nested podman cgroup.
2. `CopyToContainer` tar behavior — podman compat may differ from Docker for tar archive format.
3. `NetworkDisabled` + `tmpfs` mounts — interaction between podman networking and tmpfs.
4. `context.WithTimeout` grace period — podman's docker-compat wait may not respect the same timeout.

## POC Evidence (Without Live Cluster)

- **Build**: `cd vendor/csoj && go build ./...` PASSED (Round 1, 2026-07-03)
- **Vet**: `cd vendor/csoj && go vet ./...` PASSED
- **Config Parse**: CSOJ binary loads our `deploy/csoj/config.yaml` without config errors.
  - All config fields recognized (database, storage, cluster, node, docker host, etc.)
  - DockerConfig.Host = `tcp://dedicated-judge-runtime.hpc101-runtime.svc.cluster.local:2375`
- **Binary**: CSOJ binary builds and starts from vendored subtree.
- **DockerManager API POC** (Round 2, 2026-07-03): 9/9 tests PASS against local Podman 5.4.2.
  - Run: `DOCKER_HOST=tcp://127.0.0.1:PORT go run cmd/podman-poc/`
  - Tests: volume create/remove, container create (NanoCPUs+Memory), start, tar copy,
    exec with stdcopy multiplexed stdout/stderr, timeout cancellation, cleanup.
  - CSOJ code unchanged — DockerManager uses `client.WithHost(cfg.Host)` which accepts
    any docker-compatible endpoint.
  - Reproducibility note: POC go.mod requires go >= 1.25.0 due to Docker SDK v28.5.1
    transitive dependency on opentelemetry (otel/trace@v1.44.0). CSOJ itself
    (vendor/csoj) builds with go 1.24. The POC is a validation tool, not a build
    dependency of the platform.
- **Runtime POC** (cluster): PENDING (requires deployed dedicated-judge-runtime Service;
  escalate to cluster test when available).
