package enroll

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// trustAsSystem stands caPEM in for this machine's trust store for one test.
func trustAsSystem(t *testing.T, caPEM []byte) {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("no certificate in the bundle")
	}
	old := systemRoots
	systemRoots = func() *x509.CertPool { return pool }
	t.Cleanup(func() { systemRoots = old })
}

// A web UI on a Let's Encrypt certificate still publishes FOG's own CA,
// which did not sign it. The probe has to offer this machine's trust store
// rather than fail and send the admin looking for a file (2026-09-11 report).
func TestProbeCATrustsACertificateTheMachineTrusts(t *testing.T) {
	publicCA, sCert, sKey, _, _ := testCA(t)
	fogCA, _, _, _, _ := testCA(t)
	srv := caServer(t, tls.Certificate{Certificate: [][]byte{sCert.Raw}, PrivateKey: sKey}, fogCA)
	trustAsSystem(t, publicCA)

	probe, err := ProbeCA(context.Background(), srv.URL+"/fog")
	if err != nil {
		t.Fatalf("probe refused a server this machine trusts: %v", err)
	}
	if !probe.SystemTrust {
		t.Fatal("the probe did not report system trust")
	}
	if probe.PEM != nil || probe.Fingerprint != "" {
		t.Fatal("a system-trusted server must offer nothing to pin")
	}
	if probe.Subject != "CN=127.0.0.1" || probe.Issuer != "CN=test CA" {
		t.Fatalf("the probe did not describe the server's certificate: %q issued by %q", probe.Subject, probe.Issuer)
	}
}

// A FOG CA that is also in the machine's store -- the legacy client put it
// there -- still has to go through the fingerprint.
func TestProbeCAPrefersThePublishedCAOverTheMachineStore(t *testing.T) {
	fogCA, sCert, sKey, _, _ := testCA(t)
	srv := caServer(t, tls.Certificate{Certificate: [][]byte{sCert.Raw}, PrivateKey: sKey}, fogCA)
	trustAsSystem(t, fogCA)

	probe, err := ProbeCA(context.Background(), srv.URL+"/fog")
	if err != nil {
		t.Fatal(err)
	}
	if probe.SystemTrust || probe.Fingerprint == "" {
		t.Fatal("a server on the CA it publishes skipped the fingerprint because the machine also trusts that CA")
	}
}

// The machine's trust is for a name. Reached by a name the certificate does
// not carry, the server is not trusted, and the error says which name.
func TestProbeCASystemTrustChecksTheServerName(t *testing.T) {
	publicCA, sCert, sKey, _, _ := testCA(t)
	fogCA, _, _, _, _ := testCA(t)
	srv := caServer(t, tls.Certificate{Certificate: [][]byte{sCert.Raw}, PrivateKey: sKey}, fogCA)
	trustAsSystem(t, publicCA)

	byName := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	_, err := ProbeCA(context.Background(), byName+"/fog")
	if err == nil {
		t.Fatal("a certificate for 127.0.0.1 was trusted for localhost")
	}
	if !strings.Contains(err.Error(), "wanted to match localhost") || !strings.Contains(err.Error(), "public or corporate") {
		t.Fatalf("the error does not say the name is the problem: %v", err)
	}
}

// The client for a system-trusted server verifies the way the probe did:
// against the machine's store, and nothing else.
func TestSystemTrustClientVerifiesAgainstTheMachineStore(t *testing.T) {
	publicCA, sCert, sKey, _, _ := testCA(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(Response{Status: StatusPending, Reason: "unknown-host", RetryAfter: 1})
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{sCert.Raw}, PrivateKey: sKey}}}
	srv.StartTLS()
	defer srv.Close()

	st := &State{Dir: t.TempDir()}
	st.EnsureKey(liveID("aaaa"))
	csr, _ := st.CSR()
	req := Request{Protocol: Protocol, Identity: *st.Identity, CSRPEM: string(csr)}

	if _, err := NewSystemTrustClient(srv.URL).Enroll(context.Background(), req); err == nil {
		t.Fatal("enrolled against a server whose issuer this machine does not trust")
	}
	trustAsSystem(t, publicCA)
	r, err := NewSystemTrustClient(srv.URL).Enroll(context.Background(), req)
	if err != nil || r.Status != StatusPending {
		t.Fatalf("enroll against a server this machine trusts: %+v err=%v", r, err)
	}
}

// Only one of a pinned CA and system trust is in force, whichever was
// settled last, and the client follows it.
func TestStateHoldsOneTrust(t *testing.T) {
	dir := t.TempDir()
	st, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Config.ServerURL = "https://fog.example.org/fog"
	caPEM, _, _, _, _ := testCA(t)
	if err := st.SaveCA(caPEM); err != nil {
		t.Fatal(err)
	}
	if err := st.UseSystemTrust(); err != nil {
		t.Fatal(err)
	}
	if len(st.CA()) != 0 {
		t.Fatal("system trust left the pinned bundle behind")
	}
	reloaded, err := Load(dir)
	if err != nil || !reloaded.Config.SystemTrust {
		t.Fatalf("system trust was not remembered: %v", err)
	}
	c, err := reloaded.Client()
	if err != nil || c.tlsConfig.RootCAs != nil {
		t.Fatalf("a system-trust state built a pinned client: %v", err)
	}

	if err := reloaded.SaveCA(caPEM); err != nil {
		t.Fatal(err)
	}
	again, err := Load(dir)
	if err != nil || again.Config.SystemTrust {
		t.Fatalf("a CA file did not replace system trust: %v", err)
	}
	c, err = again.Client()
	if err != nil || c.tlsConfig.RootCAs == nil {
		t.Fatalf("a pinned state did not build a pinned client: %v", err)
	}
}
