package sshca

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"time"

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

// ParsePublicKey parses an OpenSSH authorized_keys format public key.
func ParsePublicKey(pubKeyStr string) (ssh.PublicKey, error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubKeyStr))
	if err != nil {
		return nil, fmt.Errorf("sshca: parse public key: %w", err)
	}
	return key, nil
}

// SignUserCertFromStrings is a convenience wrapper that takes public key and
// principal as strings, signs a cert, and returns the PEM-encoded result.
func (ca *CA) SignUserCertFromStrings(pubKeyStr, principal string, validHours int) (string, error) {
	pubKey, err := ParsePublicKey(pubKeyStr)
	if err != nil {
		return "", err
	}
	cert, err := ca.SignUserCert(SignUserCertRequest{
		UserPublicKey: pubKey,
		Principal:     principal,
		ValidDuration: time.Duration(validHours) * time.Hour,
	})
	if err != nil {
		return "", err
	}
	return string(MarshalCert(cert)), nil
}
