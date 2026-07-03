#!/bin/bash
# Verify daemon-level judge container hardening per plan AC-8.
#
# Run INSIDE a container spawned by CSOJ through the podman runtime.
# Checks /proc/self/status for:
#   1. CapEff (effective capabilities) — must be 0x0 (CapDrop=ALL inherited)
#   2. NoNewPrivs — must be 1 (no_new_privileges=true inherited)
#   3. Seccomp — must be 2 (SECCOMP_MODE_FILTER, seccomp profile applied)
#
# These inherit from podman's containers.conf defaults:
#   default_capabilities = []
#   add_capabilities = []
#   no_new_privileges = true
#   seccomp_profile = "/usr/share/containers/seccomp.json"

set -euo pipefail

PASS=0
FAIL=0

echo "=== hpc101-platform: Container Hardening Verification (AC-8) ==="
echo ""

# Check 1: Effective capabilities
CAP_EFF=$(grep '^CapEff:' /proc/self/status | awk '{print $2}')
echo "CapEff: $CAP_EFF"
if [[ "$CAP_EFF" == "0000000000000000" ]]; then
    echo "  [PASS] No effective capabilities (CapDrop=ALL inherited from daemon)"
    PASS=$((PASS + 1))
else
    echo "  [FAIL] Capabilities present (expected 0000000000000000)"
    echo "  Fix: set default_capabilities=[] and add_capabilities=[] in containers.conf"
    FAIL=$((FAIL + 1))
fi

# Check 2: NoNewPrivs
NO_NEW_PRIVS=$(grep '^NoNewPrivs:' /proc/self/status | awk '{print $2}')
echo "NoNewPrivs: $NO_NEW_PRIVS"
if [[ "$NO_NEW_PRIVS" == "1" ]]; then
    echo "  [PASS] NoNewPrivs=1 (no_new_privileges=true inherited from daemon)"
    PASS=$((PASS + 1))
else
    echo "  [FAIL] NoNewPrivs=$NO_NEW_PRIVS (expected 1)"
    echo "  Fix: set no_new_privileges=true in containers.conf [containers] section"
    FAIL=$((FAIL + 1))
fi

# Check 3: Seccomp mode
SECCOMP=$(grep '^Seccomp:' /proc/self/status | awk '{print $2}')
echo "Seccomp: $SECCOMP"
case "$SECCOMP" in
    2)
        echo "  [PASS] Seccomp mode 2 (SECCOMP_MODE_FILTER — profile active)"
        PASS=$((PASS + 1))
        ;;
    0)
        echo "  [FAIL] Seccomp mode 0 (SECCOMP_MODE_DISABLED)"
        echo "  Fix: set seccomp_profile in containers.conf [containers] section"
        FAIL=$((FAIL + 1))
        ;;
    1)
        echo "  [FAIL] Seccomp mode 1 (SECCOMP_MODE_STRICT — unexpected)"
        FAIL=$((FAIL + 1))
        ;;
    *)
        echo "  [FAIL] Unknown seccomp mode: $SECCOMP"
        FAIL=$((FAIL + 1))
        ;;
esac

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="

if [[ $FAIL -gt 0 ]]; then
    echo ""
    echo "AC-8 NOT SATISFIED: hardening profile not fully inherited."
    echo "Verify containers.conf on the podman runtime has:"
    echo "  [containers]"
    echo "  default_capabilities = []"
    echo "  add_capabilities = []"
    echo "  no_new_privileges = true"
    echo "  seccomp_profile = \"/usr/share/containers/seccomp.json\""
    exit 1
fi

echo ""
echo "AC-8 SATISFIED: Judge container inherits daemon-level hardening."
echo "CapDrop=ALL | NoNewPrivs | seccomp=filter — zero CSOJ code changes."
