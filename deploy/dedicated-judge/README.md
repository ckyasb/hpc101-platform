# DEC-1: Dedicated Podman Judge Node

Production path for CSOJ judged workloads when rootless Podman-in-k8s
cannot satisfy cgroup resource limits (NanoCPUs/CpusetCpus/Memory).

## Rationale

The in-cluster rootless Podman deployment (`deploy/podman-runtime/`) is the
primary path. However, Codex analysis (task4) confirmed that `cpu`/`cpuset`
cgroup controllers are not available to rootless Podman inside a restricted
k8s pod. This means CSOJ's per-submission resource limits may not be enforced.

This dedicated node path provides a production-grade alternative:
- A dedicated VM or bare-metal node running Podman with full cgroup access
- Docker-compatible TCP endpoint (`tcp://<node>:2375`) reachable from the cluster
- CSOJ's `DockerConfig.Host` points to this endpoint — zero code change
- All containers (judge + service) inherit `containers.conf` hardening

## Deployment

1. Provision a VM or physical node with:
   - Podman >= v5.8.4 (stable; avoid v6.0 CNI removal)
   - Container networking configured (CNI or netavark)
   - `containers.conf` with security hardening (see below)

2. Copy `containers.conf` to `/etc/containers/containers.conf` on the node.

3. Start Podman API service:
   ```bash
   podman system service --time=0 tcp:0.0.0.0:2375 &
   ```

4. Configure firewall to allow cluster-internal access to port 2375.

5. Point CSOJ at this endpoint by setting `DockerConfig.Host`:
   ```yaml
   DockerConfig:
     Host: "tcp://dedicated-judge-runtime.hpc101-runtime.svc.cluster.local:2375"
   ```

6. Set `HPC101_RUNTIME_ENDPOINT=tcp://dedicated-judge-runtime.hpc101-runtime.svc.cluster.local:2375` for the controller.

## containers.conf Security Profile

See `containers.conf` in this directory. Key settings:
- `default_capabilities = []` — drop all capabilities
- `no_new_privileges = true` — prevent privilege escalation
- `seccomp_profile = /usr/share/containers/seccomp.json` — default filter

## Verification

```bash
# Verify resource limits are enforced
podman run --rm --cpus 0.5 --memory 128m alpine sh -c 'cat /proc/self/status | grep -E "CapEff|NoNewPrivs|Seccomp"'

# Expected output:
# CapEff: 0000000000000000
# NoNewPrivs: 1
# Seccomp: 2
```

## Degraded Path

If neither rootless-in-k8s nor dedicated node is available, the Docker daemon
fallback is marked as degraded/non-production because it cannot guarantee
CapDrop/no-new-privileges/seccomp without host-level configuration.
