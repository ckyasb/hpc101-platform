#!/bin/sh
# Provision the platform SSH CA for hpc101-platform.
#
# Creates an Ed25519 CA keypair in PKCS8 PEM format (consumable by sshca.LoadCA)
# and creates Kubernetes Secrets in both the controller namespace (hpc101-platform,
# with ca_key + ca.pub) and the bastion namespace (hpc101-bastion, with ca.pub).
#
# The controller signs student certs with ca_key; the bastion trusts ca.pub via
# TrustedUserCAKeys. Both must come from the same keypair.
#
# Usage: ./provision-ca.sh
# Idempotent: re-running regenerates the CA and overwrites the secrets.

set -e

CTRL_NS="${HPC101_PLATFORM_NS:-hpc101-platform}"
BASTION_NS="${HPC101_BASTION_NS:-hpc101-bastion}"
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

echo "=== Generating Ed25519 CA keypair (PKCS8 PEM) ==="
# Generate Ed25519 key and convert to PKCS8 PEM (what sshca.LoadCA expects).
openssl genpkey -algorithm Ed25519 -out "$TMPDIR/ca_key" 2>/dev/null
chmod 0600 "$TMPDIR/ca_key"

# Derive the OpenSSH-format public key for TrustedUserCAKeys.
ssh-keygen -y -f "$TMPDIR/ca_key" > "$TMPDIR/ca.pub"

echo "=== Creating CA secret in controller namespace ($CTRL_NS) ==="
kubectl create namespace "$CTRL_NS" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$CTRL_NS" delete secret bastion-ca-keys --ignore-not-found
kubectl -n "$CTRL_NS" create secret generic bastion-ca-keys \
  --from-file=ca_key="$TMPDIR/ca_key" \
  --from-file=ca.pub="$TMPDIR/ca.pub"

echo "=== Creating CA public key secret in bastion namespace ($BASTION_NS) ==="
kubectl create namespace "$BASTION_NS" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$BASTION_NS" delete secret bastion-ca-keys --ignore-not-found
kubectl -n "$BASTION_NS" create secret generic bastion-ca-keys \
  --from-file=ca.pub="$TMPDIR/ca.pub"

echo "=== Done ==="
echo "Controller ($CTRL_NS): ca_key + ca.pub in secret bastion-ca-keys"
echo "Bastion   ($BASTION_NS): ca.pub in secret bastion-ca-keys"
echo ""
echo "Next: run verify-ca.sh to confirm the controller key matches the bastion public key."
