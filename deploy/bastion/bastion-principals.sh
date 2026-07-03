#!/bin/sh
# bastion-principals: AuthorizedPrincipalsCommand for hpc101-platform
# Called by sshd with %i = certificate Key ID (student principal).
#
# Queries the platform controller lease API to find the student's
# active container and returns:
#   permitopen="<container_host>:<port>" <principal>
#
# If no active lease exists, returns nothing — sshd rejects auth.
#
# Controller API (task10): GET /api/v1/leases?principal=<key_id>
# Response: {"container_host":"10.0.0.5","container_port":2222}

PRINCIPAL="$1"
CTRL="${HPC101_CONTROLLER_URL:-http://controller.hpc101-platform.svc.cluster.local:8080}"

LEASE=$(curl -s --max-time 5 "${CTRL}/api/v1/leases?principal=${PRINCIPAL}" 2>/dev/null || echo "")

if [ -z "$LEASE" ]; then
	# No lease yet — return nothing, sshd rejects
	exit 0
fi

# Parse lease response (requires jq in bastion image)
HOST=$(echo "$LEASE" | jq -r '.container_host // ""' 2>/dev/null)
PORT=$(echo "$LEASE" | jq -r '.container_port // ""' 2>/dev/null)

if [ -n "$HOST" ] && [ -n "$PORT" ]; then
	echo "permitopen=\"${HOST}:${PORT}\" ${PRINCIPAL}"
fi
exit 0
