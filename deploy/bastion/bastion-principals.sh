#!/bin/sh
# bastion-principals: AuthorizedPrincipalsCommand for hpc101-platform
# Called by sshd with %i = certificate Key ID (student principal).
#
# Returns: permitopen="<host>:<port>" <principal>
# If no active lease or invalid principal, returns nothing — sshd rejects.

PRINCIPAL="$1"

# Validate principal: alphanumeric, hyphen, underscore, 1-64 chars
if ! echo "$PRINCIPAL" | grep -qE '^[a-zA-Z0-9_-]{1,64}$'; then
	exit 0
fi

CTRL="${HPC101_CONTROLLER_URL:-http://controller.hpc101-platform.svc.cluster.local:8080}"
LEASE=$(curl -s --max-time 5 "${CTRL}/api/v1/leases?principal=${PRINCIPAL}" 2>/dev/null || echo "")

if [ -z "$LEASE" ]; then
	exit 0
fi

# Parse lease response
HOST=$(echo "$LEASE" | jq -r '.container_host // ""' 2>/dev/null)
PORT=$(echo "$LEASE" | jq -r '.container_port // ""' 2>/dev/null)
STATE=$(echo "$LEASE" | jq -r '.state // ""' 2>/dev/null)

# Only active leases
if [ "$STATE" != "Active" ]; then
	exit 0
fi

# Validate host (no shell metacharacters) and port (1-65535 integer)
if ! echo "$HOST" | grep -qE '^[a-zA-Z0-9._-]+$'; then
	exit 0
fi
if ! echo "$PORT" | grep -qE '^[1-9][0-9]{0,4}$'; then
	exit 0
fi
if [ "$PORT" -gt 65535 ] 2>/dev/null; then
	exit 0
fi

echo "permitopen=\"${HOST}:${PORT}\" ${PRINCIPAL}"
exit 0
