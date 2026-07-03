#!/bin/sh
# Verify the platform SSH CA trust relationship between controller and bastion.
#
# Confirms that the controller's mounted ca_key (PKCS8 PEM, loaded by sshca.LoadCA)
# produces a public key matching the bastion's TrustedUserCAKeys ca.pub, and that a
# cert signed with ca_key verifies against ca.pub by fingerprint comparison.
#
# Usage: ./verify-ca.sh

set -e

CTRL_NS="${HPC101_PLATFORM_NS:-hpc101-platform}"
BASTION_NS="${HPC101_BASTION_NS:-hpc101-bastion}"
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

echo "=== Extracting CA material from both namespaces ==="
kubectl -n "$CTRL_NS" get secret bastion-ca-keys -o jsonpath='{.data.ca_key}' | base64 -d > "$TMPDIR/ca_key"
# Restrict permissions immediately so ssh-keygen accepts the private key.
chmod 600 "$TMPDIR/ca_key"
kubectl -n "$BASTION_NS" get secret bastion-ca-keys -o jsonpath='{.data.ca\.pub}' | base64 -d > "$TMPDIR/bastion_ca.pub"

echo "=== Checking controller ca_key is readable PKCS8 ==="
openssl pkey -in "$TMPDIR/ca_key" -noout 2>/dev/null || {
  echo "FAIL: controller ca_key is not a valid PKCS8 PEM private key"
  exit 1
}
echo "PASS: ca_key is valid PKCS8"

echo "=== Deriving public key from controller ca_key ==="
ssh-keygen -y -f "$TMPDIR/ca_key" > "$TMPDIR/controller_ca.pub"

echo "=== Comparing controller-derived pub vs bastion-trusted ca.pub ==="
if diff -q "$TMPDIR/controller_ca.pub" "$TMPDIR/bastion_ca.pub" >/dev/null; then
  echo "PASS: controller ca_key matches bastion ca.pub"
else
  echo "FAIL: controller ca_key does not match bastion ca.pub"
  echo "controller: $(cat "$TMPDIR/controller_ca.pub")"
  echo "bastion:    $(cat "$TMPDIR/bastion_ca.pub")"
  exit 1
fi

echo "=== Signing a test cert with controller ca_key ==="
ssh-keygen -t ed25519 -f "$TMPDIR/user_key" -N "" -q
chmod 600 "$TMPDIR/user_key"
ssh-keygen -s "$TMPDIR/ca_key" -I test-user -n testuser -V +5m "$TMPDIR/user_key.pub" >/dev/null 2>&1 || {
  echo "FAIL: could not sign cert with controller ca_key"
  exit 1
}

echo "=== Verifying cert signature against bastion-trusted CA ==="
# Parse the cert's Signing CA line and extract the SHA256: fingerprint token.
# OpenSSH output: "Signing CA: ED25519 SHA256:abcd..."
# Use grep + sed to robustly extract the SHA256: token regardless of key type/field position.
CERT_SIGNER=$(ssh-keygen -L -f "$TMPDIR/user_key-cert.pub" | grep 'Signing CA:' | grep -oE 'SHA256:[A-Za-z0-9+/=]+')
# Parse the bastion CA fingerprint from ssh-keygen -lf (field 2 is SHA256:...).
BASTION_FP=$(ssh-keygen -lf "$TMPDIR/bastion_ca.pub" | grep -oE 'SHA256:[A-Za-z0-9+/=]+')

if [ -z "$CERT_SIGNER" ]; then
  echo "FAIL: could not extract cert signer fingerprint from certificate"
  exit 1
fi
if [ -z "$BASTION_FP" ]; then
  echo "FAIL: could not extract bastion CA fingerprint"
  exit 1
fi

if [ "$CERT_SIGNER" = "$BASTION_FP" ]; then
  echo "PASS: cert signer fingerprint matches bastion CA ($BASTION_FP)"
else
  echo "FAIL: cert signer ($CERT_SIGNER) != bastion CA ($BASTION_FP)"
  exit 1
fi

echo "=== CA trust verification complete ==="
