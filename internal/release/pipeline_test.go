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

// TestTheManifestNeverOffersAnInstallerAsTheBinary pins the shape of a real
// release directory, which the test above does not: a release publishes an
// MSI next to fog-agent-windows-amd64.exe, and both are "windows amd64".
//
// The manifest describes the file the agent RENAMES OVER ITSELF. Find()
// takes the first match and has no way to prefer one of two, so a manifest
// carrying both entries does not fail anywhere an operator would see it --
// it makes which file a Windows fleet installs depend on the order the
// release step happened to list its files in. Half the time that is an 11 MB
// installer swapped in as fog-agent.exe: it downloads, its hash verifies
// because the hash is correct, the service will not start, and the host
// reverts fifteen minutes later having reported nothing useful.
func TestTheManifestNeverOffersAnInstallerAsTheBinary(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl is not installed; this test drives the real signing tooling")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	run := func(name string, args ...string) ([]byte, error) {
		cmd := exec.Command(filepath.Join(root, "build", name), args...)
		cmd.Dir = dir
		return cmd.CombinedOutput()
	}

	// A release directory as build/cross.sh and build/msi.sh leave it.
	var files []string
	for _, name := range []string{
		"fog-agent-windows-amd64.exe",
		"fog-agent-0.1.2-x64.msi",
		"fog-agent-linux-amd64",
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("stand-in for "+name), 0o755); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	if out, err := run("mint-signing-ca.sh", "--dir", dir); err != nil {
		t.Fatalf("mint: %v\n%s", err, out)
	}
	args := append([]string{"--dir", dir, "--out", dir, "--version", "0.1.2",
		"--sequence", "1", "--url-base", "https://releases.example.invalid/v0.1.2"}, files...)
	if out, err := run("sign-manifest.sh", args...); err != nil {
		t.Fatalf("sign-manifest: %v\n%s", err, out)
	}

	body, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	arts := m.Versions["0.1.2"].Artifacts

	// One entry per platform. Asserted over every platform rather than by
	// looking for the MSI, because the defect is the duplicate, and the MSI
	// is only the way it turns up today.
	seen := map[string]string{}
	for _, a := range arts {
		key := a.OS + "/" + a.Arch
		if prev, dup := seen[key]; dup {
			t.Fatalf("two artifacts claim %s (%s and %s); Find() would take whichever was listed first",
				key, prev, a.URL)
		}
		seen[key] = a.URL
	}
	if len(arts) != 2 {
		t.Errorf("want the two real binaries, got %d artifacts: %+v", len(arts), arts)
	}

	// And what a Windows amd64 agent is handed is the executable it can
	// actually rename over itself.
	a, err := m.Find("0.1.2", "windows", "amd64")
	if err != nil {
		t.Fatalf("windows/amd64 must still be offered: %v", err)
	}
	if filepath.Ext(a.URL) == ".msi" {
		t.Errorf("a Windows agent would swap an installer in as its own binary: %s", a.URL)
	}
	if filepath.Base(a.URL) != "fog-agent-windows-amd64.exe" {
		t.Errorf("windows/amd64 should be the exe, got %s", a.URL)
	}
}

// TestAMergedManifestStillVerifiesAndStillOffersTheOlderVersion pins the
// downgrade path, which is the whole recovery story of design 0015 sections
// 9 and 11: the failure local rollback cannot catch is a build that
// installs, starts and polls perfectly well and then behaves badly, and the
// only fix is the server naming an older version.
//
// That needs the manifest to still describe the older version. A manifest
// per release describes exactly one, and Find() looks a version up by exact
// key -- so without --merge, naming anything but the newest release answers
// no_artifact and the recovery cannot be expressed at all.
//
// The second half matters as much as the first: the merged manifest is
// written by jq rather than printf, and the signature is over whatever was
// written. If those two ever disagree the agent reports signature_invalid,
// which names the wrong thing entirely.
func TestAMergedManifestStillVerifiesAndStillOffersTheOlderVersion(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl is not installed; this test drives the real signing tooling")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed; --merge needs it")
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
	artifact := filepath.Join(dir, "fog-agent-linux-amd64")
	if err := os.WriteFile(artifact, []byte("stand-in"), 0o755); err != nil {
		t.Fatal(err)
	}
	run("mint-signing-ca.sh", "--dir", dir)

	published := filepath.Join(dir, "published.json")
	sign := func(version, sequence string) {
		t.Helper()
		run("sign-manifest.sh", "--dir", dir, "--out", dir,
			"--version", version, "--sequence", sequence,
			"--url-base", "https://releases.example.invalid/v"+version,
			"--merge", published, artifact)
		// What the release publishes becomes what the next one merges.
		b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(published, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The first release has nothing to merge, which must not be an error.
	sign("0.4.2", "47")
	sign("0.5.0", "48")

	body, err := os.ReadFile(published)
	if err != nil {
		t.Fatal(err)
	}
	envJSON, err := os.ReadFile(filepath.Join(dir, "manifest.json.sig"))
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(envJSON, &env); err != nil {
		t.Fatal(err)
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
		t.Fatalf("a merged manifest must still verify against the root: %v", err)
	}
	if m.Sequence != 48 {
		t.Errorf("the merged manifest must carry the NEW sequence, got %d", m.Sequence)
	}
	if _, err := m.Find("0.5.0", "linux", "amd64"); err != nil {
		t.Errorf("the version just released must be offered: %v", err)
	}
	// The point of the whole exercise.
	if _, err := m.Find("0.4.2", "linux", "amd64"); err != nil {
		t.Fatalf("the previous version must still be offered, or a fleet cannot be moved back: %v", err)
	}
}
