package sshca

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// GenerateUserKey generates a new RSA 3072-bit keypair for a student.
// Returns the signer (for SSH agent) and the public key (for CA signing).
// In production, students provide their own public key; this is for
// platform-managed keypairs and testing.
func GenerateUserKey() (ssh.Signer, ssh.PublicKey, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return nil, nil, fmt.Errorf("sshca: generate user key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("sshca: new signer from user key: %w", err)
	}
	return signer, signer.PublicKey(), nil
}

// MarshalCert marshals an SSH certificate to the OpenSSH wire format
// suitable for writing to an id_ecdsa-cert.pub or similar file.
func MarshalCert(cert *ssh.Certificate) []byte {
	return ssh.MarshalAuthorizedKey(cert)
}
