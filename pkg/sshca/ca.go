// Package sshca provides SSH Certificate Authority operations for the
// hpc101-platform bastion. Generates and loads Ed25519 CA keys, signs
// short-lived user certificates with per-student identity.
//
// Certificates carry the valid OpenSSH critical option:
//   - force-command="/bin/false"  → no interactive shell on bastion
//
// Container target binding is NOT in the certificate (permitopen is
// not a valid user-cert option). It is enforced by the bastion's
// AuthorizedPrincipalsCommand, which returns permitopen=host:port.
//
// The bastion's sshd_config trusts this CA (TrustedUserCAKeys),
// disables raw authorized_keys (AuthorizedKeysFile none), and enforces
// cert-only authentication. Students use:
//   ssh -J bastion user@container-host -p <port>
package sshca

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

// CA holds the SSH certificate authority key and configuration.
type CA struct {
	signer    ssh.Signer
	PublicKey ssh.PublicKey
	rawKey    interface{} // underlying crypto private key for PEM export
}

// GenerateCA creates a new Ed25519 CA keypair and returns the CA signer.
func GenerateCA() (*CA, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("sshca: generate key: %w", err)
	}

	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("sshca: new signer: %w", err)
	}

	return &CA{signer: signer, PublicKey: signer.PublicKey(), rawKey: priv}, nil
}

// LoadCA loads a CA from a PEM-encoded Ed25519 private key file.
func LoadCA(privateKeyPath string) (*CA, error) {
	data, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("sshca: read key: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("sshca: no PEM block found in %s", privateKeyPath)
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("sshca: parse private key: %w", err)
	}

	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, fmt.Errorf("sshca: new signer from loaded key: %w", err)
	}

	return &CA{signer: signer, PublicKey: signer.PublicKey()}, nil
}

// SavePrivateKey writes the CA's private key in PEM (PKCS8) format.
// Only works for CA created via GenerateCA (which preserves the raw key).
// For CA created via LoadCA, the key is already on disk.
func (ca *CA) SavePrivateKey(path string) error {
	if ca.rawKey == nil {
		return fmt.Errorf("sshca: raw key not available; key was loaded, not generated — already on disk")
	}
	der, err := x509.MarshalPKCS8PrivateKey(ca.rawKey)
	if err != nil {
		return fmt.Errorf("sshca: marshal pkcs8: %w", err)
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	return os.WriteFile(path, pem.EncodeToMemory(block), 0600)
}

// SavePublicKey writes the CA's public key in OpenSSH authorized_keys format.
func (ca *CA) SavePublicKey(path string) error {
	data := ssh.MarshalAuthorizedKey(ca.PublicKey)
	return os.WriteFile(path, data, 0644)
}

// SignUserCertRequest carries the parameters needed to sign a user certificate.
type SignUserCertRequest struct {
	// UserPublicKey is the student's SSH public key.
	UserPublicKey ssh.PublicKey
	// Principal is the identity (e.g., student username).
	Principal string
	// ValidDuration is how long the certificate is valid.
	ValidDuration time.Duration
}

// SignUserCert signs a short-lived SSH user certificate with:
//   - Key ID = principal
//   - Critical option: force-command="/bin/false" (valid OpenSSH user-cert option;
//     prevents interactive shell on bastion)
//   - Valid from now until now+ValidDuration
//
// Container target binding is NOT in the certificate. It is enforced by the
// bastion's AuthorizedPrincipalsCommand, which queries the platform lease store
// and returns "permitopen=\"host:port\" <principal>". This is the correct
// mechanism — permitopen is an authorized_keys/sshd_config option, not a
// valid user certificate critical option, and would cause cert rejection.
func (ca *CA) SignUserCert(req SignUserCertRequest) (*ssh.Certificate, error) {
	if req.ValidDuration <= 0 {
		return nil, fmt.Errorf("sshca: ValidDuration must be positive")
	}

	serial := uint64(time.Now().UnixNano())

	cert := &ssh.Certificate{
		Key:             req.UserPublicKey,
		Serial:          serial,
		CertType:        ssh.UserCert,
		KeyId:           req.Principal,
		ValidPrincipals: []string{req.Principal},
		ValidAfter:      uint64(time.Now().Unix()),
		ValidBefore:     uint64(time.Now().Add(req.ValidDuration).Unix()),
		Permissions: ssh.Permissions{
			CriticalOptions: map[string]string{
				"force-command": "/bin/false",
			},
		},
	}

	if err := cert.SignCert(rand.Reader, ca.signer); err != nil {
		return nil, fmt.Errorf("sshca: sign cert: %w", err)
	}

	return cert, nil
}
