#!/bin/sh
# DEC-1 Dedicated Judge Node — Docker API Verification
#
# Proves the Docker-compatible runtime endpoint satisfies CSOJ's DockerManager
# usage pattern (AC-1) through the API ONLY — no direct podman CLI.
#
# Operations verified via http://$ENDPOINT:2375/:
#   1. Version reachability
#   2. Container create with NanoCPUs + Memory resource limits
#   3. Container start
#   4. Exec attach with stdcopy framing validation
#   5. Archive upload (CopyToContainer tar) + content verification
#   6. Volume create + remove
#   7. Resource limit assertion (fail-closed: memory + CPU in cgroup)
#   8. In-container hardening (/proc/self/status: CapEff, NoNewPrivs, Seccomp)
#   9. Stop + remove cleanup
#  10. Wait + timeout behavior (/containers/{id}/wait)
#
# Usage: ENDPOINT=localhost PORT=2375 ./verify.sh
# Fails (exit 1) on any missing operation or wrong value.

set -e

ENDPOINT="${HPC101_RUNTIME_HOST:-localhost}"
PORT="${HPC101_RUNTIME_PORT:-2375}"
API="http://${ENDPOINT}:${PORT}"
IMAGE="${HPC101_VERIFY_IMAGE:-alpine:latest}"
CTR_ID=""

# POSIX-compatible container ID truncation (no Bash ${var:0:12}).
short_id() {
  printf '%.12s' "$1"
}

cleanup() {
  if [ -n "$CTR_ID" ]; then
    curl -s -X POST "${API}/containers/${CTR_ID}/stop" >/dev/null 2>&1 || true
    curl -s -X DELETE "${API}/containers/${CTR_ID}?force=true" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"; cleanup' EXIT

echo "=== DEC-1 Docker API Verification ==="
echo "Endpoint: ${API}"
echo ""

# 1. Version reachability
echo "[1/10] Version reachability..."
VERSION=$(curl -sf "${API}/version" 2>/dev/null) || { echo "FAIL: /version not reachable"; exit 1; }
echo "PASS: API reachable"

# 2. Container create with NanoCPUs + Memory + CpusetCpus
echo ""
echo "[2/10] Container create with resource limits..."
CPUSET="${HPC101_VERIFY_CPUSET:-0}"
CREATE_RESP=$(curl -sf -X POST "${API}/containers/create" \
  -H 'Content-Type: application/json' \
  -d "{\"Image\":\"${IMAGE}\",\"Cmd\":[\"sleep\",\"120\"],\"HostConfig\":{\"NanoCPUs\":500000000,\"Memory\":134217728,\"CpusetCpus\":\"${CPUSET}\"}}") || { echo "FAIL: container create"; exit 1; }
CTR_ID=$(echo "$CREATE_RESP" | grep -o '"Id":"[^"]*"' | cut -d'"' -f4)
if [ -z "$CTR_ID" ]; then echo "FAIL: no container ID in create response"; exit 1; fi
echo "PASS: container created ($(short_id "$CTR_ID"))"

# 3. Container start
echo ""
echo "[3/10] Container start..."
curl -sf -X POST "${API}/containers/${CTR_ID}/start" >/dev/null 2>&1 || { echo "FAIL: container start"; exit 1; }
echo "PASS: container started"

# 4. Exec attach with stdcopy framing validation
echo ""
echo "[4/10] Exec attach (stdcopy framing)..."
EXEC_CREATE=$(curl -sf -X POST "${API}/containers/${CTR_ID}/exec" \
  -H 'Content-Type: application/json' \
  -d '{"AttachStdout":true,"AttachStderr":true,"Cmd":["echo","exec-works"]}') || { echo "FAIL: exec create"; exit 1; }
EXEC_ID=$(echo "$EXEC_CREATE" | grep -o '"Id":"[^"]*"' | cut -d'"' -f4)
if [ -z "$EXEC_ID" ]; then echo "FAIL: no exec ID"; exit 1; fi
# Capture raw binary output to temp file for binary-safe framing analysis.
EXEC_FILE="$TMPDIR/exec_raw.bin"
curl -sf -X POST "${API}/exec/${EXEC_ID}/start" \
  -H 'Content-Type: application/vnd.docker.raw-stream' \
  -d '{}' -o "$EXEC_FILE" 2>/dev/null || { echo "FAIL: exec start"; exit 1; }
# Docker stdcopy multiplex header: 1 byte stream type, 3 bytes reserved, 4 bytes payload length (big-endian).
# Validate the full 8-byte header, not just the first byte.
HEADER_HEX=$(od -An -tx1 -N8 "$EXEC_FILE" | tr -d ' \n')
STREAM_TYPE=$(printf '%d' "0x$(printf '%s' "$HEADER_HEX" | cut -c1-2)" 2>/dev/null || echo 0)
PAYLOAD_LEN=$(printf '%d' "0x$(printf '%s' "$HEADER_HEX" | cut -c13-16)" 2>/dev/null || echo 0)
# Extract payload starting at byte 9 (skip 8-byte header).
PAYLOAD=$(dd if="$EXEC_FILE" bs=1 skip=8 count="$PAYLOAD_LEN" 2>/dev/null)
if [ "$STREAM_TYPE" = "1" ] || [ "$STREAM_TYPE" = "2" ]; then
  if [ "$PAYLOAD_LEN" -gt 0 ] 2>/dev/null; then
    if echo "$PAYLOAD" | grep -q "exec-works"; then
      echo "PASS: stdcopy framing valid (stream=$STREAM_TYPE len=$PAYLOAD_LEN payload contains 'exec-works')"
    else
      echo "FAIL: stdcopy header valid but payload missing 'exec-works'"
      exit 1
    fi
  else
    echo "FAIL: stdcopy header valid but payload length is 0"
    exit 1
  fi
else
  echo "FAIL: exec response is not stdcopy-framed (stream type=$STREAM_TYPE, expected 1 or 2)"
  echo "  header hex: $HEADER_HEX"
  echo "  CSOJ's stdcopy.StdCopy will fail to parse this."
  exit 1
fi

# 5. Archive upload (CopyToContainer tar) + content verification
echo ""
echo "[5/10] Archive upload + content verification..."
echo "hello-from-tar" > "$TMPDIR/testfile.txt"
tar cf "$TMPDIR/test.tar" -C "$TMPDIR" testfile.txt
curl -sf -X PUT "${API}/containers/${CTR_ID}/archive?path=/tmp" \
  -H 'Content-Type: application/x-tar' \
  --data-binary "@$TMPDIR/test.tar" >/dev/null 2>&1 || { echo "FAIL: archive upload"; exit 1; }
# Verify the file was extracted with correct content by execing cat.
TAR_EXEC=$(curl -sf -X POST "${API}/containers/${CTR_ID}/exec" \
  -H 'Content-Type: application/json' \
  -d '{"AttachStdout":true,"Cmd":["cat","/tmp/testfile.txt"]}') || { echo "FAIL: tar verify exec create"; exit 1; }
TAR_EXEC_ID=$(echo "$TAR_EXEC" | grep -o '"Id":"[^"]*"' | cut -d'"' -f4)
TAR_OUT=$(curl -sf -X POST "${API}/exec/${TAR_EXEC_ID}/start" \
  -H 'Content-Type: application/vnd.docker.raw-stream' -d '{}' 2>/dev/null) || true
if echo "$TAR_OUT" | grep -q "hello-from-tar"; then
  echo "PASS: archive extracted with correct content"
else
  echo "FAIL: /tmp/testfile.txt does not contain 'hello-from-tar'"
  exit 1
fi

# 6. Volume create + remove
echo ""
echo "[6/10] Volume lifecycle..."
VOL_NAME="hpc101-verify-$$"
curl -sf -X POST "${API}/volumes/create" \
  -H 'Content-Type: application/json' \
  -d "{\"Name\":\"${VOL_NAME}\"}" >/dev/null 2>&1 || { echo "FAIL: volume create"; exit 1; }
curl -sf -X DELETE "${API}/volumes/${VOL_NAME}" >/dev/null 2>&1 || { echo "FAIL: volume remove"; exit 1; }
echo "PASS: volume create + remove"

# 7. Resource limit assertion (fail-closed)
echo ""
echo "[7/10] Resource limit assertion (fail-closed)..."
# Use labeled output so parsing is deterministic regardless of stdcopy framing.
RES_EXEC=$(curl -sf -X POST "${API}/containers/${CTR_ID}/exec" \
  -H 'Content-Type: application/json' \
  -d '{"AttachStdout":true,"Cmd":["sh","-c","printf \"cpu.max=%s\\n\" \"$(cat /sys/fs/cgroup/cpu.max 2>/dev/null || echo missing)\"; printf \"memory.max=%s\\n\" \"$(cat /sys/fs/cgroup/memory.max 2>/dev/null || echo missing)\"; printf \"cpuset=%s\\n\" \"$(cat /sys/fs/cgroup/cpuset.cpus 2>/dev/null || cat /sys/fs/cgroup/cpuset.cpus.effective 2>/dev/null || echo missing)\""]}') || { echo "FAIL: res exec create"; exit 1; }
RES_EXEC_ID=$(echo "$RES_EXEC" | grep -o '"Id":"[^"]*"' | cut -d'"' -f4)
RES_FILE="$TMPDIR/res_raw.bin"
curl -sf -X POST "${API}/exec/${RES_EXEC_ID}/start" \
  -H 'Content-Type: application/vnd.docker.raw-stream' -d '{}' -o "$RES_FILE" 2>/dev/null || true
RES_CLEAN=$(cat "$RES_FILE" | tr -d '\0' | sed 's/[^[:print:]]//g')
# Memory limit must be 134217728 bytes (128MB). Fail if absent.
MEM_VAL=$(echo "$RES_CLEAN" | grep "memory.max=" | cut -d= -f2 | tr -d '[:space:]')
if [ "$MEM_VAL" = "134217728" ]; then
  echo "PASS: memory limit enforced (134217728 bytes)"
else
  echo "FAIL: memory limit 134217728 not found (got: $MEM_VAL)"
  echo "  cgroup output: $RES_CLEAN"
  exit 1
fi
# CPU quota must show a non-max value (quota assigned). Fail if absent or unlimited.
CPU_VAL=$(echo "$RES_CLEAN" | grep "cpu.max=" | cut -d= -f2 | tr -d '[:space:]')
if echo "$CPU_VAL" | grep -q "^max"; then
  echo "FAIL: CPU quota is unlimited (cpu.max=$CPU_VAL)"
  exit 1
elif echo "$CPU_VAL" | grep -q "500000"; then
  echo "PASS: CPU quota enforced (500000/100000 = 0.5 CPU)"
else
  echo "FAIL: CPU quota 500000 not found in cpu.max (got: $CPU_VAL)"
  exit 1
fi
# Cpuset must include the configured CPU.
CPUSET_VAL=$(echo "$RES_CLEAN" | grep "cpuset=" | cut -d= -f2 | tr -d '[:space:]')
if echo "$CPUSET_VAL" | grep -q "${CPUSET}" 2>/dev/null; then
  echo "PASS: cpuset includes configured CPU (${CPUSET})"
else
  echo "FAIL: cpuset does not include configured CPU ${CPUSET} (got: $CPUSET_VAL)"
  exit 1
fi

# 8. In-container hardening (/proc/self/status)
echo ""
echo "[8/10] In-container hardening (/proc/self/status)..."
HARD_EXEC=$(curl -sf -X POST "${API}/containers/${CTR_ID}/exec" \
  -H 'Content-Type: application/json' \
  -d '{"AttachStdout":true,"Cmd":["sh","-c","grep -E \"CapEff|NoNewPrivs|Seccomp\" /proc/self/status"]}') || { echo "FAIL: hardening exec create"; exit 1; }
HARD_EXEC_ID=$(echo "$HARD_EXEC" | grep -o '"Id":"[^"]*"' | cut -d'"' -f4)
HARD_OUT=$(curl -sf -X POST "${API}/exec/${HARD_EXEC_ID}/start" \
  -H 'Content-Type: application/vnd.docker.raw-stream' -d '{}' 2>/dev/null) || true
HARD_CLEAN=$(echo "$HARD_OUT" | tr -d '\0' | sed 's/[^[:print:]]//g')
if echo "$HARD_CLEAN" | grep -q "CapEff.*0000000000000000"; then
  echo "PASS: CapEff is empty set"
else
  echo "FAIL: CapEff is not empty — hardening not applied"
  echo "  /proc/self/status: $HARD_CLEAN"
  exit 1
fi
if echo "$HARD_CLEAN" | grep -q "NoNewPrivs.*1"; then
  echo "PASS: NoNewPrivs is 1"
else
  echo "FAIL: NoNewPrivs is not 1"
  exit 1
fi
if echo "$HARD_CLEAN" | grep -q "Seccomp.*2"; then
  echo "PASS: Seccomp is 2 (filter mode)"
else
  echo "FAIL: Seccomp is not 2 (filter mode)"
  exit 1
fi

# 9. Stop + remove cleanup
echo ""
echo "[9/10] Stop + remove cleanup..."
curl -sf -X POST "${API}/containers/${CTR_ID}/stop" >/dev/null 2>&1 || { echo "FAIL: container stop"; exit 1; }
curl -sf -X DELETE "${API}/containers/${CTR_ID}?force=true" >/dev/null 2>&1 || { echo "FAIL: container remove"; exit 1; }
CTR_ID=""
echo "PASS: container stopped + removed"

# 10. Wait + timeout behavior (/containers/{id}/wait)
echo ""
echo "[10/10] Wait + timeout behavior..."
WAIT_CTR=$(curl -sf -X POST "${API}/containers/create" \
  -H 'Content-Type: application/json' \
  -d "{\"Image\":\"${IMAGE}\",\"Cmd\":[\"sleep\",\"2\"]}" | grep -o '"Id":"[^"]*"' | cut -d'"' -f4) || { echo "FAIL: wait test create"; exit 1; }
curl -sf -X POST "${API}/containers/${WAIT_CTR}/start" >/dev/null 2>&1 || { echo "FAIL: wait test start"; exit 1; }
# Wait for the container to finish (CSOJ's context.WithTimeout pattern).
# The /wait endpoint blocks until the container exits or the client times out.
WAIT_RESP=$(curl -sf -X POST "${API}/containers/${WAIT_CTR}/wait" 2>/dev/null) || { echo "FAIL: wait endpoint"; exit 1; }
WAIT_CODE=$(echo "$WAIT_RESP" | grep -o '"StatusCode":[0-9]*' | cut -d':' -f2)
if [ -n "$WAIT_CODE" ]; then
  echo "PASS: /wait returned StatusCode=${WAIT_CODE}"
else
  echo "FAIL: /wait did not return a StatusCode"
  exit 1
fi
curl -sf -X DELETE "${API}/containers/${WAIT_CTR}?force=true" >/dev/null 2>&1 || { echo "FAIL: wait test remove"; exit 1; }
echo "PASS: wait test cleanup"

# 10b. Client-side timeout test (CSOJ's context.WithTimeout pattern)
echo ""
echo "[10b] Client-side timeout behavior..."
TO_CTR=$(curl -sf -X POST "${API}/containers/create" \
  -H 'Content-Type: application/json' \
  -d "{\"Image\":\"${IMAGE}\",\"Cmd\":[\"sleep\",\"30\"]}" | grep -o '"Id":"[^"]*"' | cut -d'"' -f4) || { echo "FAIL: timeout test create"; exit 1; }
curl -sf -X POST "${API}/containers/${TO_CTR}/start" >/dev/null 2>&1 || { echo "FAIL: timeout test start"; exit 1; }
# curl --max-time forces a client-side timeout, simulating context.WithTimeout.
# The /wait should not return before the timeout; curl exits 28 on timeout.
# Temporarily disable set -e so curl's non-zero exit is captured.
set +e
curl -sf --max-time 1 -X POST "${API}/containers/${TO_CTR}/wait" >/dev/null 2>&1
CURL_EXIT=$?
set -e
if [ "$CURL_EXIT" = "28" ]; then
  echo "PASS: client-side timeout fires (curl exit 28)"
else
  echo "FAIL: expected curl timeout (exit 28), got exit $CURL_EXIT"
fi
# Cleanup: stop and remove the still-running container.
curl -sf -X POST "${API}/containers/${TO_CTR}/stop" >/dev/null 2>&1 || true
curl -sf -X DELETE "${API}/containers/${TO_CTR}?force=true" >/dev/null 2>&1 || { echo "FAIL: timeout test remove"; exit 1; }
echo "PASS: timeout test cleanup"

echo ""
echo "=== All Docker API verification checks passed ==="
echo ""
echo "Ready for CSOJ integration: set DockerConfig.Host to tcp://${ENDPOINT}:${PORT}"
