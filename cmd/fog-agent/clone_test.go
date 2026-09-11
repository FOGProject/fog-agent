package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/identity"
)

func machine(uuid string) identity.Host {
	var h identity.Host
	h.SystemUUID = uuid
	h.SystemSerial = "serial-" + uuid
	return h
}

func stubIdentity(t *testing.T, h identity.Host) {
	t.Helper()
	was := readIdentity
	readIdentity = func() identity.Host { return h }
	t.Cleanup(func() { readIdentity = was })
}

// agentServer answers an enrollment with "pending" and a poll with 401, so
// `run --once` ends after one request either way, and records which it got.
type agentServer struct {
	*httptest.Server
	ca    []byte
	mu    sync.Mutex
	paths map[string]bool
	csr   string
}

func newAgentServer(t *testing.T) *agentServer {
	t.Helper()
	s := &agentServer{paths: map[string]bool{}}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.paths[r.URL.Path] = true
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/agent/v1/enroll":
			var req enroll.Request
			_ = json.Unmarshal(body, &req)
			s.mu.Lock()
			s.csr = req.CSRPEM
			s.mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"status":"pending","reason":"known-host-no-agent"}`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"status":"error"}`)
		}
	}))
	t.Cleanup(s.Close)
	s.ca = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
	return s
}

func (s *agentServer) hit(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths[path]
}

// capturedState writes what an image of an enrolled machine carries: a key
// made for that machine, a certificate issued to it, and the host-specific
// config that came with the enrollment. Facts and sessions are off so a
// poll does not gather from the machine running the test.
func capturedState(t *testing.T, s *agentServer) (dir string, key *ecdsa.PublicKey) {
	t.Helper()
	dir = t.TempDir()
	st, err := enroll.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Config.ServerURL = s.URL
	if err := st.SaveCA(s.ca); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureKey(machine("golden")); err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "golden"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &st.Key.PublicKey, st.Key)
	if err != nil {
		t.Fatal(err)
	}
	st.Config.AppliedRevision = "golden-revision"
	st.Config.SoftwareHash = "golden-software"
	st.Config.FactsDisabled = true
	st.Config.SessionsDisabled = true
	if err := st.SaveIssued(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 105); err != nil {
		t.Fatal(err)
	}
	return dir, &st.Key.PublicKey
}

// The forum report (topic 18242): every machine deployed from one image
// renamed itself to the machine the image was captured from. The clone guard
// only ran when there was no certificate, and an image carries one.
func TestAClonedStateEnrollsInsteadOfPollingAsTheOriginal(t *testing.T) {
	s := newAgentServer(t)
	dir, golden := capturedState(t, s)
	stubIdentity(t, machine("clone"))

	err := runAgent(context.Background(), []string{"--dir", dir, "--once"})

	if s.hit("/agent/v1/poll") {
		t.Fatal("the clone polled with the captured machine's certificate")
	}
	if !s.hit("/agent/v1/enroll") {
		t.Fatalf("the clone never enrolled (run: %v)", err)
	}
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	blk, _ := pem.Decode([]byte(s.csr))
	if blk == nil {
		t.Fatal("the enrollment carried no CSR")
	}
	csr, err := x509.ParseCertificateRequest(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if pub, ok := csr.PublicKey.(*ecdsa.PublicKey); !ok || pub.Equal(golden) {
		t.Fatal("the clone enrolled with the captured machine's key")
	}
	st, err := enroll.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Config.HostID != 0 || st.Config.AppliedRevision != "" || st.Config.SoftwareHash != "" || st.Config.FactsDisabled {
		t.Fatalf("the captured host's state survived the new key: %+v", st.Config)
	}
	if st.Config.ServerURL != s.URL {
		t.Fatalf("the server URL was lost with the host state: %q", st.Config.ServerURL)
	}
}

// The other half of the gate: running the guard on every start must not
// send the machine the state was made on back through enrollment.
func TestTheOriginalMachineKeepsItsCertificateAndPolls(t *testing.T) {
	s := newAgentServer(t)
	dir, _ := capturedState(t, s)
	stubIdentity(t, machine("golden"))

	_ = runAgent(context.Background(), []string{"--dir", dir, "--once"})

	if s.hit("/agent/v1/enroll") {
		t.Fatal("the machine the state was made on enrolled again")
	}
	if !s.hit("/agent/v1/poll") {
		t.Fatal("the machine the state was made on never polled")
	}
}
