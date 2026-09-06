package enroll

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// caServer stands up a TLS listener presenting srvCert and publishing
// publish at the path a FOG server publishes its CA on.
func caServer(t *testing.T, srvCert tls.Certificate, publish []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fog"+caPublishedPath {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Write(publish)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{srvCert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// The ordinary FOG server: its web certificate is issued by the CA it
// publishes, so the probe can offer that CA as the anchor and the
// fingerprint it reports is the one the web UI shows.
func TestProbeCAReportsThePublishedAnchor(t *testing.T) {
	caPEM, sCert, sKey, _, _ := testCA(t)
	srv := caServer(t, tls.Certificate{Certificate: [][]byte{sCert.Raw}, PrivateKey: sKey}, caPEM)

	probe, err := ProbeCA(context.Background(), srv.URL+"/fog")
	if err != nil {
		t.Fatalf("probe failed against a server that publishes its own CA: %v", err)
	}
	if string(probe.PEM) != string(caPEM) {
		t.Fatal("the probe did not return the bundle the server published")
	}
	want, err := FingerprintOf(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Fingerprint != want {
		t.Fatalf("fingerprint %s, want %s", probe.Fingerprint, want)
	}
	// The format is the thing being compared by eye against the web UI, so
	// it is part of the contract, not a detail.
	if len(probe.Fingerprint) != 95 || !strings.Contains(probe.Fingerprint, ":") ||
		probe.Fingerprint != strings.ToUpper(probe.Fingerprint) {
		t.Fatalf("fingerprint is not the upper-case colon-separated form the UI prints: %q", probe.Fingerprint)
	}
	if _, err := NewClient(probe.ServerURL, probe.PEM); err != nil {
		t.Fatalf("the probed bundle is not usable as a trust anchor: %v", err)
	}
}

// A server whose web UI runs on a public or corporate certificate still
// publishes FOG's internal CA at the same path. Pinning it would leave the
// agent unable to verify a single connection, so the probe refuses and says
// which case this is.
func TestProbeCARefusesACAThatDidNotSignTheServer(t *testing.T) {
	_, sCert, sKey, _, _ := testCA(t)
	otherCA, _, _, _, _ := testCA(t) // a different CA entirely
	srv := caServer(t, tls.Certificate{Certificate: [][]byte{sCert.Raw}, PrivateKey: sKey}, otherCA)

	_, err := ProbeCA(context.Background(), srv.URL+"/fog")
	if err == nil {
		t.Fatal("a CA that did not sign the server's certificate was accepted")
	}
	if !strings.Contains(err.Error(), "public or corporate") {
		t.Fatalf("the error does not point at the case it is: %v", err)
	}
}

func TestProbeCAReportsAServerThatPublishesNothing(t *testing.T) {
	_, sCert, sKey, _, _ := testCA(t)
	srv := caServer(t, tls.Certificate{Certificate: [][]byte{sCert.Raw}, PrivateKey: sKey}, nil)
	// Publishing at a path the handler 404s.
	_, err := ProbeCA(context.Background(), srv.URL+"/nowhere")
	if err == nil || !strings.Contains(err.Error(), caPublishedPath) {
		t.Fatalf("a server with no published CA should name the path it looked at: %v", err)
	}
}

// An admin pastes the fingerprint out of the web UI, or a script carries it
// through a config file that lower-cased it or stripped the colons. All of
// those are the same digest and all of them have to match.
func TestSameFingerprintIgnoresFormattingButNotValue(t *testing.T) {
	const ui = "50:02:7A:40:9A:B0:F4:B9:57:CE:BF:94:26:21:31:E1:38:42:F1:8E:A6:0F:24:D8:64:D3:F5:5D:0F:EB:C5:C4"
	for _, same := range []string{
		ui,
		strings.ToLower(ui),
		strings.ReplaceAll(ui, ":", ""),
		strings.ReplaceAll(ui, ":", " "),
		strings.ReplaceAll(strings.ToLower(ui), ":", "-"),
	} {
		if !SameFingerprint(same, ui) {
			t.Fatalf("%q should match the same digest", same)
		}
	}
	if SameFingerprint("", ui) || SameFingerprint(ui, "") {
		t.Fatal("an empty fingerprint must never match")
	}
	if SameFingerprint(strings.Replace(ui, "50", "51", 1), ui) {
		t.Fatal("a different digest matched")
	}
}
