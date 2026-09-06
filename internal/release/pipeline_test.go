package release

import (
	"crypto/x509"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The Go tests elsewhere in this package mint their fixtures with
// crypto/x509, which proves the verifier is self-consistent and nothing
// more. This one drives the actual release tooling -- the same two shell
// scripts a release runs -- and verifies what they produce. A signature
// scheme where the signer and the verifier were written from the same
// assumption and never introduced is a scheme that passes its tests and
// fails on the first real release.
func TestTheReleaseScriptsProduceSomethingThisPackageAccepts(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl is not installed; this test drives the real signing tooling")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(filepath.Join(root, "build", name), args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", name, err, out)
		}
	}

	// A stand-in for a built binary. Its bytes do not matter; that the
	// manifest describes exactly these bytes is the whole point.
	artifact := filepath.Join(dir, "fog-agent-linux-amd64")
	payload := []byte("#!/bin/sh\necho this is not really an agent\n")
	if err := os.WriteFile(artifact, payload, 0o755); err != nil {
		t.Fatal(err)
	}

	run("mint-signing-ca.sh", "--dir", dir)
	run("sign-manifest.sh", "--dir", dir, "--out", dir,
		"--version", "0.4.2", "--sequence", "47",
		"--url-base", "https://releases.example.invalid/agent/v0.4.2",
		artifact)

	body, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	envJSON, err := os.ReadFile(filepath.Join(dir, "manifest.json.sig"))
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(envJSON, &env); err != nil {
		t.Fatalf("the envelope the script wrote is not readable: %v", err)
	}

	rootPEM, err := os.ReadFile(filepath.Join(dir, "root.crt"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	for _, c := range parsePEMCerts(rootPEM) {
		pool.AddCert(c)
	}

	m, err := Verify(body, &env, pool, time.Now())
	if err != nil {
		t.Fatalf("a manifest signed by the release tooling must verify: %v", err)
	}
	if m.Sequence != 47 || m.Channel != "stable" {
		t.Fatalf("manifest did not carry what the script was told: %+v", m)
	}

	a, err := m.Find("0.4.2", "linux", "amd64")
	if err != nil {
		t.Fatalf("the script must record the platform it was handed: %v", err)
	}
	if a.Size != int64(len(payload)) {
		t.Errorf("size %d, artifact is %d bytes", a.Size, len(payload))
	}

	// The digest in the manifest must be the digest of the real file.
	f, err := os.Open(artifact)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := CopyVerified(io.Discard, f, a); err != nil {
		t.Fatalf("the artifact does not match the hash the script recorded for it: %v", err)
	}

	// And the tamper case, against the real signer rather than a Go one:
	// change a byte of the signed manifest and it must stop verifying.
	tampered := append([]byte{}, body...)
	tampered[len(tampered)/2] ^= 0x20
	if _, err := Verify(tampered, &env, pool, time.Now()); err == nil {
		t.Fatal("a modified manifest verified against a real openssl signature")
	}
}
