package enroll

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FOGProject/fog-agent/internal/identity"
)

func liveID(uuid string) identity.Host {
	var h identity.Host
	h.SystemUUID = uuid
	h.SystemSerial = "19P4L63"
	h.MACs = []string{"cc:48:3a:5e:11:aa"}
	return h
}

func TestEnsureKeyKeepsKeyForSameMachineAndRotatesOnClone(t *testing.T) {
	dir := t.TempDir()
	st, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Key != nil {
		t.Fatal("fresh dir should have no key")
	}
	regen, err := st.EnsureKey(liveID("aaaa"))
	if err != nil || !regen {
		t.Fatalf("first EnsureKey: regen=%v err=%v", regen, err)
	}
	first := st.Key.PublicKey
	if err := st.SaveIssued([]byte("CERT"), 105); err != nil {
		t.Fatal(err)
	}

	// Same machine, different MACs (docked): key must survive, so must the cert.
	st2, _ := Load(dir)
	docked := liveID("aaaa")
	docked.MACs = []string{"00:11:22:33:44:55"}
	regen, _ = st2.EnsureKey(docked)
	if regen || !st2.Key.PublicKey.Equal(&first) || string(st2.Cert) != "CERT" || st2.Config.HostID != 105 {
		t.Fatalf("same machine: regen=%v keyEqual=%v cert=%q host=%d", regen, st2.Key.PublicKey.Equal(&first), st2.Cert, st2.Config.HostID)
	}

	// Cloned disk on a different machine: new key, certificate gone.
	st3, _ := Load(dir)
	regen, _ = st3.EnsureKey(liveID("bbbb"))
	if !regen || st3.Key.PublicKey.Equal(&first) || st3.Cert != nil {
		t.Fatalf("clone: regen=%v keyEqual=%v cert=%q", regen, st3.Key.PublicKey.Equal(&first), st3.Cert)
	}
	if _, err := os.Stat(filepath.Join(dir, certFile)); !os.IsNotExist(err) {
		t.Fatal("cert file should be removed on key rotation")
	}
	if fi, _ := os.Stat(filepath.Join(dir, keyFile)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", fi.Mode().Perm())
	}
}

func TestCSRIsSignedByTheKey(t *testing.T) {
	st := &State{Dir: t.TempDir()}
	if _, err := st.EnsureKey(liveID("aaaa")); err != nil {
		t.Fatal(err)
	}
	csrPEM, err := st.CSR()
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(csrPEM)
	csr, err := x509.ParseCertificateRequest(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatal(err)
	}
	if !csr.PublicKey.(*ecdsa.PublicKey).Equal(&st.Key.PublicKey) {
		t.Fatal("CSR public key is not the state key")
	}
}

// testCA builds a throwaway CA and a server cert for 127.0.0.1 so the client
// can be exercised against a real TLS listener with its own trust bundle.
func testCA(t *testing.T) (caPEM []byte, serverCert *x509.Certificate, serverKey *ecdsa.PrivateKey, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) {
	caKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	caCert, _ = x509.ParseCertificate(caDER)
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	serverKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	sTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	sDER, _ := x509.CreateCertificate(rand.Reader, sTmpl, caCert, &serverKey.PublicKey, caKey)
	serverCert, _ = x509.ParseCertificate(sDER)
	return
}

func TestEnrollPendingThenIssuedAndRejectsUntrustedServer(t *testing.T) {
	caPEM, sCert, sKey, _, _ := testCA(t)
	calls := 0
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/enroll" || r.Method != http.MethodPost {
			http.Error(w, "wrong route", 404)
			return
		}
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Protocol != Protocol || req.CSRPEM == "" {
			http.Error(w, "bad request", 400)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(Response{Status: StatusPending, Reason: "unknown-host", RetryAfter: 1})
			return
		}
		json.NewEncoder(w).Encode(Response{Status: StatusIssued, HostID: 105, CertificatePEM: "CERT"})
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{sCert.Raw}, PrivateKey: sKey}}}
	srv.StartTLS()
	defer srv.Close()

	st := &State{Dir: t.TempDir()}
	st.EnsureKey(liveID("aaaa"))
	csr, _ := st.CSR()
	req := Request{Protocol: Protocol, Identity: *st.Identity, CSRPEM: string(csr)}

	c, err := NewClient(srv.URL, caPEM)
	if err != nil {
		t.Fatal(err)
	}
	r1, err := c.Enroll(context.Background(), req)
	if err != nil || r1.Status != StatusPending || r1.Reason != "unknown-host" {
		t.Fatalf("first: %+v err=%v", r1, err)
	}
	r2, err := c.Enroll(context.Background(), req)
	if err != nil || r2.Status != StatusIssued || r2.HostID != 105 {
		t.Fatalf("second: %+v err=%v", r2, err)
	}

	// A client trusting a different CA must refuse to talk to this server.
	otherCA, _, _, _, _ := testCA(t)
	c2, _ := NewClient(srv.URL, otherCA)
	if _, err := c2.Enroll(context.Background(), req); err == nil {
		t.Fatal("enroll succeeded against a server the client should not trust")
	}
	if _, err := NewClient(srv.URL, nil); err == nil {
		t.Fatal("empty CA bundle must be refused")
	}
}

// TestPollPresentsCertificateAndTreats401AsUnauthorized pins the two halves
// of the authenticated channel: the client offers the issued certificate on
// the TLS handshake (a server that asks for one sees it), and a 401 comes
// back as ErrUnauthorized so the service loop knows to enroll again.
func TestPollPresentsCertificateAndTreats401AsUnauthorized(t *testing.T) {
	caPEM, serverCert, serverKey, caCert, caKey := testCA(t)
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/poll" || r.Method != http.MethodPost {
			http.Error(w, "wrong route", 404)
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":"client certificate required"}`)
			return
		}
		fmt.Fprintf(w, `{"status":"ok","protocol":1,"host":{"id":7,"name":"%s"},"capabilities":["x"],"poll_interval":42}`, r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{serverCert.Raw}, PrivateKey: serverKey}},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    pool,
	}
	srv.StartTLS()
	defer srv.Close()

	client, err := NewClient(srv.URL, caPEM)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Poll(context.Background(), PollRequest{AgentVersion: "t"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("poll without a certificate: want ErrUnauthorized, got %v", err)
	}

	// An agent certificate issued by the test CA, as the server would.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "fog-agent host 7"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})...)
	if err := client.UseCertificate(certPEM, key); err != nil {
		t.Fatal(err)
	}
	resp, err := client.Poll(context.Background(), PollRequest{AgentVersion: "t"})
	if err != nil {
		t.Fatalf("poll with the certificate: %v", err)
	}
	if resp.Host.ID != 7 || resp.Host.Name != "fog-agent host 7" || resp.PollInterval != 42 || len(resp.Capabilities) != 1 {
		t.Fatalf("unexpected poll answer: %+v", resp)
	}
}

// TestLoadCreatesTheStateDirectory pins the fix for `run` on a fresh
// machine: the first write is the CA bundle, and it must not depend on the
// key step having created the directory.
func TestLoadCreatesTheStateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh", "state")
	st, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCA([]byte("x")); err != nil {
		t.Fatalf("first write into a fresh state dir: %v", err)
	}
}
