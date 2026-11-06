package main

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/identity"
)

// oldCA is the pinned bundle an earlier install left behind. It is a PEM
// block so --ca accepts the same bytes again.
var oldCA = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("old")})

var staleErr = &tls.CertificateVerificationError{Err: errors.New("x509: unknown authority")}

// stubCheckTrust answers forgetStaleTrust with err.
func stubCheckTrust(t *testing.T, err error) {
	t.Helper()
	old := checkTrust
	checkTrust = func(context.Context, *enroll.State) error { return err }
	t.Cleanup(func() { checkTrust = old })
}

// stubProbeFails makes every question to the server fail, and counts them.
func stubProbeFails(t *testing.T) *int {
	t.Helper()
	calls := new(int)
	old := probeCA
	probeCA = func(context.Context, string) (*enroll.CAProbe, error) {
		*calls++
		return nil, errors.New("the server did not answer")
	}
	t.Cleanup(func() { probeCA = old })
	return calls
}

// stubProbeCounts answers with p and counts the questions.
func stubProbeCounts(t *testing.T, p *enroll.CAProbe) *int {
	t.Helper()
	calls := new(int)
	old := probeCA
	probeCA = func(context.Context, string) (*enroll.CAProbe, error) {
		*calls++
		return p, nil
	}
	t.Cleanup(func() { probeCA = old })
	return calls
}

// enrolledState is a state directory an earlier install left behind: trust,
// a key and an issued certificate. system picks system trust over oldCA.
func enrolledState(t *testing.T, system bool) (dir string) {
	t.Helper()
	dir = t.TempDir()
	st, err := enroll.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Config.ServerURL = trustedProbe.ServerURL
	// Load reads the certificate only beside a key, as an earlier install
	// leaves them.
	var live identity.Host
	live.SystemUUID = "aaaa"
	if _, err := st.EnsureKey(live); err != nil {
		t.Fatal(err)
	}
	if system {
		err = st.UseSystemTrust()
	} else {
		err = st.SaveCA(oldCA)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveIssued([]byte("old cert"), 42); err != nil {
		t.Fatal(err)
	}
	return dir
}

func caFileOn(t *testing.T, dir string) []byte {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
	return b
}

func writeCA(t *testing.T, der string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte(der)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertKept(t *testing.T, dir string, st *enroll.State) {
	t.Helper()
	if string(caFileOn(t, dir)) != string(oldCA) || st.Config.SystemTrust {
		t.Fatal("the stored CA changed")
	}
	if string(st.Cert) != "old cert" || st.Config.HostID != 42 {
		t.Fatal("the issued certificate was dropped")
	}
}

// (a) A pinned CA that no longer verifies the server is forgotten, trust is
// settled again like a first install, and the certificate issued under the
// old trust is dropped so the token is used.
func TestSettleTrustStalePinIsSettledAgain(t *testing.T) {
	dir := enrolledState(t, false)
	stubCheckTrust(t, staleErr)
	stubProbe(t, trustedProbe)

	st, err := settleTrust(flagsFor(t, "--dir", dir), &sayer{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Config.SystemTrust {
		t.Fatal("trust was not settled again")
	}
	if caFileOn(t, dir) != nil {
		t.Fatal("the stale CA file is still there")
	}
	if len(st.Cert) != 0 || st.Config.HostID != 0 {
		t.Fatalf("the certificate issued under the old trust was kept: host %d", st.Config.HostID)
	}
}

// (b) A CA that still verifies the server changes nothing, and the server is
// not asked what to trust.
func TestSettleTrustVerifyingTrustIsLeftAlone(t *testing.T) {
	dir := enrolledState(t, false)
	stubCheckTrust(t, nil)
	stubProbe(t, nil)

	st, err := settleTrust(flagsFor(t, "--dir", dir), &sayer{})
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, dir, st)
}

// (c) A server that does not answer is not a reason to forget anything.
func TestSettleTrustUnreachableServerLeavesTrustAlone(t *testing.T) {
	dir := enrolledState(t, false)
	stubCheckTrust(t, errors.New("dial tcp: connection refused"))
	stubProbe(t, nil)

	st, err := settleTrust(flagsFor(t, "--dir", dir), &sayer{})
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, dir, st)
}

// (d) Trust settled again to the same CA keeps the enrollment, so the host
// keeps its identity.
func TestSettleTrustSameCAKeepsTheCertificate(t *testing.T) {
	dir := enrolledState(t, false)
	stubCheckTrust(t, staleErr)
	stubProbe(t, nil)
	caFile := filepath.Join(t.TempDir(), "same.pem")
	if err := os.WriteFile(caFile, oldCA, 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := settleTrust(flagsFor(t, "--dir", dir, "--ca", caFile), &sayer{})
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, dir, st)
}

// (e) A stored CA file with no certificate in it is settled again too.
func TestSettleTrustNoCertificateInStoredCA(t *testing.T) {
	dir := enrolledState(t, false)
	stubCheckTrust(t, enroll.ErrNoCACertificate)
	stubProbe(t, trustedProbe)

	st, err := settleTrust(flagsFor(t, "--dir", dir), &sayer{})
	if err != nil || !st.Config.SystemTrust || len(st.Cert) != 0 {
		t.Fatalf("an empty stored CA was not settled again: %v", err)
	}
}

// (f) Stored system trust that no longer verifies is settled again: the
// server is asked what to trust.
func TestSettleTrustStaleSystemTrustIsSettledAgain(t *testing.T) {
	dir := enrolledState(t, true)
	stubCheckTrust(t, staleErr)
	calls := stubProbeCounts(t, trustedProbe)

	st, err := settleTrust(flagsFor(t, "--dir", dir), &sayer{})
	if err != nil || !st.Config.SystemTrust {
		t.Fatalf("settle: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("the server was asked %d times what to trust, want 1", *calls)
	}
}

// (g) With no fingerprint or CA file given, new trust that cannot be settled
// puts the old trust back and setup carries on with it, so an MSI upgrade
// does not start failing.
func TestSettleTrustFailedResettleRestoresTheOldTrust(t *testing.T) {
	dir := enrolledState(t, false)
	stubCheckTrust(t, staleErr)
	calls := stubProbeFails(t)

	st, err := settleTrust(flagsFor(t, "--dir", dir), &sayer{})
	if err != nil {
		t.Fatalf("an upgrade with no properties failed: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("the server was asked %d times, want 1", *calls)
	}
	assertKept(t, dir, st)
}

// (h) An admin who gave a fingerprint asked for new trust, so a failure is
// returned, and the old trust is still put back.
// Stored system trust comes back the same way when settling again fails.
func TestSettleTrustFailedResettleRestoresSystemTrust(t *testing.T) {
	dir := enrolledState(t, true)
	stubCheckTrust(t, staleErr)
	stubProbeFails(t)

	if _, err := settleTrust(flagsFor(t, "--dir", dir), &sayer{}); err != nil {
		t.Fatalf("an upgrade with no properties failed: %v", err)
	}
	re, err := enroll.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !re.Config.SystemTrust || len(re.CA()) != 0 {
		t.Fatal("system trust was not restored")
	}
	if string(re.Cert) != "old cert" {
		t.Fatal("the issued certificate was dropped")
	}
}

// A failed settle with --server puts the stored server URL back with the
// trust, as if setup had never run.
func TestSettleTrustFailedResettleRestoresTheServer(t *testing.T) {
	dir := enrolledState(t, false)
	stubCheckTrust(t, staleErr)
	stubProbeFails(t)

	_, err := settleTrust(flagsFor(t, "--dir", dir, "--server", "https://new.example.org/fog",
		"--ca-fingerprint", "AA:BB"), &sayer{})
	if err == nil {
		t.Fatal("a failed settle with a fingerprint given was hidden")
	}
	re, lerr := enroll.Load(dir)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if re.Config.ServerURL != trustedProbe.ServerURL {
		t.Fatalf("the stored server became %q", re.Config.ServerURL)
	}
	assertKept(t, dir, re)
}

func TestSettleTrustFailedResettleWithFingerprintIsAnError(t *testing.T) {
	dir := enrolledState(t, false)
	stubCheckTrust(t, staleErr)
	stubProbeFails(t)

	_, err := settleTrust(flagsFor(t, "--dir", dir, "--ca-fingerprint", "AA:BB"), &sayer{})
	if err == nil {
		t.Fatal("a failed settle with a fingerprint given was hidden")
	}
	re, lerr := enroll.Load(dir)
	if lerr != nil {
		t.Fatal(lerr)
	}
	assertKept(t, dir, re)
}

// (i) A CA file that changes the CA while the old trust still verifies the
// server keeps the enrollment, as before this check existed.
func TestSettleTrustNewCAFileWhileTrustVerifiesKeepsTheCertificate(t *testing.T) {
	dir := enrolledState(t, false)
	stubCheckTrust(t, nil)
	stubProbe(t, nil)

	st, err := settleTrust(flagsFor(t, "--dir", dir, "--ca", writeCA(t, "new")), &sayer{})
	if err != nil {
		t.Fatal(err)
	}
	if string(st.CA()) == string(oldCA) {
		t.Fatal("--ca did not replace the CA")
	}
	if string(st.Cert) != "old cert" || st.Config.HostID != 42 {
		t.Fatal("the certificate was dropped though the old trust verified the server")
	}
}
