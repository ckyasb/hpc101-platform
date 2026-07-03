package sshca

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestGenerateCA(t *testing.T) {
	ca, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	if ca.PublicKey == nil {
		t.Fatal("CA PublicKey is nil")
	}
	if ca.PublicKey.Type() != ssh.KeyAlgoED25519 {
		t.Errorf("expected ed25519 CA key, got %s", ca.PublicKey.Type())
	}
}

func TestSaveAndLoadCA(t *testing.T) {
	ca, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	tmpDir := t.TempDir()
	privPath := tmpDir + "/ca"
	pubPath := tmpDir + "/ca.pub"

	if err := ca.SavePrivateKey(privPath); err != nil {
		t.Fatalf("SavePrivateKey: %v", err)
	}
	if err := ca.SavePublicKey(pubPath); err != nil {
		t.Fatalf("SavePublicKey: %v", err)
	}

	loaded, err := LoadCA(privPath)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}
	if loaded.PublicKey == nil {
		t.Fatal("loaded CA PublicKey is nil")
	}
}

func TestSignUserCert(t *testing.T) {
	ca, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	_, userPub, err := GenerateUserKey()
	if err != nil {
		t.Fatalf("GenerateUserKey: %v", err)
	}

	cert, err := ca.SignUserCert(SignUserCertRequest{
		UserPublicKey: userPub,
		Principal:     "student-42",
		ValidDuration: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("SignUserCert: %v", err)
	}

	// Check certificate fields
	if cert.CertType != ssh.UserCert {
		t.Errorf("expected UserCert, got %d", cert.CertType)
	}
	if cert.KeyId != "student-42" {
		t.Errorf("expected KeyId 'student-42', got %q", cert.KeyId)
	}
	if len(cert.ValidPrincipals) != 1 || cert.ValidPrincipals[0] != "student-42" {
		t.Errorf("expected ValidPrincipals ['student-42'], got %v", cert.ValidPrincipals)
	}
	if cert.ValidAfter == 0 {
		t.Error("ValidAfter is zero")
	}
	if cert.ValidBefore <= cert.ValidAfter {
		t.Error("ValidBefore <= ValidAfter")
	}
	if cert.Serial == 0 {
		t.Error("Serial is zero")
	}

	// Check critical options: only force-command (not permitopen)
	forceCmd, ok := cert.Permissions.CriticalOptions["force-command"]
	if !ok || forceCmd != "/bin/false" {
		t.Errorf("expected force-command=/bin/false, got %v", cert.Permissions.CriticalOptions)
	}
	if _, has := cert.Permissions.CriticalOptions["permitopen"]; has {
		t.Error("critical option 'permitopen' must not be present — not a valid user-cert option")
	}
}

func TestSignUserCertRejectsZeroDuration(t *testing.T) {
	ca, _ := GenerateCA()
	_, userPub, _ := GenerateUserKey()
	_, err := ca.SignUserCert(SignUserCertRequest{
		UserPublicKey: userPub,
		Principal:     "test",
		ValidDuration: 0,
	})
	if err == nil {
		t.Error("expected error for zero ValidDuration")
	}
}

func TestCertValidPrincipals(t *testing.T) {
	ca, _ := GenerateCA()
	_, userPub, _ := GenerateUserKey()

	cert, err := ca.SignUserCert(SignUserCertRequest{
		UserPublicKey: userPub,
		Principal:     "alice",
		ValidDuration: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("SignUserCert: %v", err)
	}

	// Cert must only be valid for the specified principal
	found := false
	for _, p := range cert.ValidPrincipals {
		if p == "alice" {
			found = true
		}
		if p == "bob" {
			t.Error("cert valid for unexpected principal 'bob'")
		}
	}
	if !found {
		t.Error("cert not valid for 'alice'")
	}
}

func TestCertMarshaling(t *testing.T) {
	ca, _ := GenerateCA()
	_, userPub, _ := GenerateUserKey()

	cert, err := ca.SignUserCert(SignUserCertRequest{
		UserPublicKey: userPub,
		Principal:     "alice",
		ValidDuration: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("SignUserCert: %v", err)
	}

	marshaled := MarshalCert(cert)
	if len(marshaled) == 0 {
		t.Fatal("MarshalCert returned empty bytes")
	}

	// Parse back the marshaled key to verify it round-trips
	pub, _, _, _, err := ssh.ParseAuthorizedKey(marshaled)
	if err != nil {
		t.Fatalf("ParseAuthorizedKey: %v", err)
	}
	parsedCert, ok := pub.(*ssh.Certificate)
	if !ok {
		t.Fatalf("marshaled key is not a certificate, got type %T", pub)
	}
	if parsedCert.KeyId != "alice" {
		t.Errorf("round-trip KeyId mismatch: got %q", parsedCert.KeyId)
	}
}

// TestNoUnsupportedCriticalOptions verifies that no unknown critical options
// are present, which would cause OpenSSH to reject the certificate.
func TestNoUnsupportedCriticalOptions(t *testing.T) {
	ca, _ := GenerateCA()
	_, userPub, _ := GenerateUserKey()

	cert, err := ca.SignUserCert(SignUserCertRequest{
		UserPublicKey: userPub,
		Principal:     "alice",
		ValidDuration: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("SignUserCert: %v", err)
	}

	// Only known valid critical options for OpenSSH user certs:
	// force-command, source-address, permit-agent-forwarding,
	// permit-port-forwarding, permit-pty, permit-user-rc, permit-X11-forwarding
	validOpts := map[string]bool{
		"force-command":            true,
		"source-address":           true,
		"permit-agent-forwarding":  true,
		"permit-port-forwarding":   true,
		"permit-pty":               true,
		"permit-user-rc":           true,
		"permit-X11-forwarding":    true,
	}

	for opt := range cert.Permissions.CriticalOptions {
		if !validOpts[opt] {
			t.Errorf("cert contains unsupported critical option %q — OpenSSH will reject", opt)
		}
	}

	for opt := range cert.Permissions.Extensions {
		t.Errorf("cert contains extension %q with value %q — extensions are allowed but should be intentional",
			opt, cert.Permissions.Extensions[opt])
	}
}

// TestSSHKeygenL verifies that the certificate can be inspected by ssh-keygen -L.
// This is a build-system test: it requires ssh-keygen on PATH.
func TestSSHKeygenL(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not found; skipping certificate inspection test")
	}

	ca, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	tmpDir := t.TempDir()
	caPubPath := tmpDir + "/ca.pub"
	if err := ca.SavePublicKey(caPubPath); err != nil {
		t.Fatalf("SavePublicKey: %v", err)
	}

	_, userPub, err := GenerateUserKey()
	if err != nil {
		t.Fatalf("GenerateUserKey: %v", err)
	}

	cert, err := ca.SignUserCert(SignUserCertRequest{
		UserPublicKey: userPub,
		Principal:     "student-42",
		ValidDuration: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("SignUserCert: %v", err)
	}

	certPath := tmpDir + "/id_ed25519-cert.pub"
	if err := os.WriteFile(certPath, MarshalCert(cert), 0644); err != nil {
		t.Fatalf("WriteFile cert: %v", err)
	}

	cmd := exec.Command("ssh-keygen", "-L", "-f", certPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-keygen -L failed: %v\nOutput: %s", err, output)
	}

	outStr := string(output)
	if outStr == "" {
		t.Error("ssh-keygen -L produced empty output")
	}
	t.Logf("ssh-keygen -L output:\n%s", outStr)
}
