#!/bin/sh
# bastion-principals: AuthorizedPrincipalsCommand for hpc101-platform
# Called by sshd with %u = the principal (student username from cert).
#
# Queries the platform controller lease API to find the student's
# active container and returns:
#   permitopen="<container_host>:<port>" <principal>
#
# If no active lease exists, returns nothing — sshd rejects auth.
#
# Controller API (task10): GET /api/v1/leases/<principal>
# Response: {"container_host":"10.0.0.5","container_port":2222}
#
# Until the controller is implemented, this script returns nothing
# (no active lease → no auth).

PRINCIPAL="$1"
CONTROLLER_URL="${HPC101_CONTROLLER_URL:-http://controller.hpc101-platform.svc.cluster.local:8080}"

LEASE=$(curl -s --max-time 5 "${CONTROLLER_URL}/api/v1/leases/${PRINCIPAL}" 2>/dev/null || echo "")

if [ -z "$LEASE" ]; then
	exit 0
fi

# Parse the lease response — requires jq or similar
# For now, returns nothing until task10 implements the lease store
exit 0
