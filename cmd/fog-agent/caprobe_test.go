package main

import (
	"context"
	"encoding/pem"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

// trustedProbe is what the probe reports for a web UI on a Let's Encrypt
// certificate that this machine trusts.
var trustedProbe = &enroll.CAProbe{
	SystemTrust: true,
	Subject:     "CN=fog.example.org",
	Issuer:      "CN=R12,O=Let's Encrypt,C=US",
	ServerURL:   "https://fog.example.org/fog",
}

// stubProbe answers acquireCA with p. A nil p fails the test if the server
// is asked at all.
func stubProbe(t *testing.T, p *enroll.CAProbe) {
	t.Helper()
	old := probeCA
	probeCA = func(context.Context, string) (*enroll.CAProbe, error) {
		if p == nil {
			t.Fatal("the server was asked what to trust when that was already settled")
		}
		return p, nil
	}
	t.Cleanup(func() { probeCA = old })
}

func flagsFor(t *testing.T, args ...string) commonFlags {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := addCommonFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return f
}

// A server on a certificate this machine trusts needs nobody to confirm
// anything, so an unattended install settles on the trust store and
// remembers it. The next start does not ask the server again.
func TestOpenStateSettlesAndRemembersSystemTrust(t *testing.T) {
	dir := t.TempDir()
	stubProbe(t, trustedProbe)
	st, err := openState(flagsFor(t, "--server", trustedProbe.ServerURL, "--dir", dir))
	if err != nil {
		t.Fatalf("an unattended install refused a server this machine trusts: %v", err)
	}
	if !st.Config.SystemTrust || len(st.CA()) != 0 {
		t.Fatal("openState did not settle on system trust alone")
	}

	stubProbe(t, nil)
	st, err = openState(flagsFor(t, "--dir", dir))
	if err != nil || !st.Config.SystemTrust {
		t.Fatalf("system trust was not remembered: %v", err)
	}
}

// An agent that pinned FOG's CA never moves to the machine's store by
// itself, whatever the server's certificate becomes.
func TestOpenStateNeverReplacesAPinnedCA(t *testing.T) {
	dir := t.TempDir()
	st, err := enroll.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Config.ServerURL = trustedProbe.ServerURL
	if err := st.SaveCA([]byte("pinned")); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveConfig(); err != nil {
		t.Fatal(err)
	}

	stubProbe(t, nil)
	st, err = openState(flagsFor(t, "--dir", dir))
	if err != nil || st.Config.SystemTrust || string(st.CA()) != "pinned" {
		t.Fatalf("a pinned CA did not stay pinned: %v", err)
	}
}

// A fingerprint says the install expected FOG's own CA. Meeting a server on
// a certificate the machine trusts instead is refused, not quietly
// accepted, and nothing is settled.
func TestAcquireCARefusesAFingerprintForAMachineTrustedServer(t *testing.T) {
	stubProbe(t, trustedProbe)
	caPEM, system, err := acquireCA(flagsFor(t, "--ca-fingerprint", "50:02:7A"), trustedProbe.ServerURL)
	if err == nil || system || caPEM != nil {
		t.Fatalf("a fingerprint was dropped in favor of system trust: system=%v err=%v", system, err)
	}
}

// A CA file given later replaces system trust.
func TestOpenStateCAFileReplacesSystemTrust(t *testing.T) {
	dir := t.TempDir()
	stubProbe(t, trustedProbe)
	if _, err := openState(flagsFor(t, "--server", trustedProbe.ServerURL, "--dir", dir)); err != nil {
		t.Fatal(err)
	}

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")}), 0o600); err != nil {
		t.Fatal(err)
	}
	stubProbe(t, nil)
	st, err := openState(flagsFor(t, "--dir", dir, "--ca", caFile))
	if err != nil || st.Config.SystemTrust || len(st.CA()) == 0 {
		t.Fatalf("a CA file did not replace system trust: %v", err)
	}
}
