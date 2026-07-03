#!/bin/bash
# Verify daemon-level judge container hardening per plan AC-8.
#
# Usage: run INSIDE a judge container spawned by CSOJ through the podman runtime.
# Checks:
#   1. CapEff (effective capabilities) — must be empty (CapDrop=ALL inherited)
#   2. NoNewPrivs — must be 1 (no_new_privileges=true inherited)
#   3. Seccomp — must be 2 (SECCOMP_MODE_FILTER, meaning seccomp profile applied)
#
# Expected: all three checks PASS if podman's containers.conf has:
#   default_capabilities = []
#   add_capabilities = []
#   no_new_privileges = true
#   seccomp = default (or equivalent seccomp_profile)

set -euo pipefail

echo "=== hpc101-platform: Container Hardening Verification ==="

# Check 1: Effective capabilities
CAP_EFF=$(grep '^CapEff:' /proc/self/status | awk '{print $2}')
echo "CapEff: $CAP_EFF"
if [[ "$CAP_EFF" == "0000000000000000" ]] || [[ "$CAP_EFF" == "0" ]]; then
    echo "  [PASS] No effective capabilities (CapDrop=ALL inherited from daemon profile)"
else
    CAP_DECODED=$(cat /proc/self/status | grep '^CapEff:')
    echo "  [FAIL] Capabilities present: $CAP_DECODED"
    echo "  Ensure podman containers.conf has: default_capabilities = []"
    exit 1
fi

# Check 2: NoNewPrivs
NO_NEW_PRIVS=$(grep '^NoNewPrivs:' /proc/self/status | awk '{print $2}')
echo "NoNewPrivs: $NO_NEW_PRIVS"
if [[ "$NO_NEW_PRIVS" == "1" ]]; then
    echo "  [PASS] NoNewPrivs=1 (no_new_privileges=true inherited from daemon profile)"
else
    echo "  [FAIL] NoNewPrivs=$NO_NEW_PRIVS (expected 1)"
    echo "  Ensure podman containers.conf has: no_new_privileges = true"
    exit 1
fi

# Check 3: Seccomp mode
SECCOMP=$(grep '^Seccomp:' /proc/self/status | awk '{print $2}')
echo "Seccomp: $SECCOMP"
case "$SECCOMP" in
    2)
        echo "  [PASS] Seccomp mode 2 (SECCOMP_MODE_FILTER — profile applied)"
        ;;
    0)
        echo "  [FAIL] Seccomp mode 0 (SECCOMP_MODE_DISABLED)"
        echo "  Ensure podman containers.conf has seccomp profile configured"
        exit 1
        ;;
    1)
        echo "  [FAIL] Seccomp mode 1 (SECCOMP_MODE_STRICT)"
        echo "  Strict mode may be acceptable but filter (2) is expected"
        exit 1
        ;;
    *)
        echo "  [FAIL] Unknown seccomp mode: $SECCOMP"
        exit 1
        ;;
esac

echo ""
echo "=== All hardening checks PASSED ==="
echo "Judge container inherits daemon-level: CapDrop=ALL, NoNewPrivs, Seccomp"
echo "This satisfies plan AC-8 without modifying CSOJ source code."
