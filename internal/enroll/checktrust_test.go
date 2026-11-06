package enroll

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// tlsServer serves HTTPS on a certificate the test CA issued.
func tlsServer(t *testing.T) (srv *httptest.Server, caPEM []byte) {
	t.Helper()
	caPEM, sCert, sKey, _, _ := testCA(t)
	srv = httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{sCert.Raw}, PrivateKey: sKey}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, caPEM
}

func isVerifyError(err error) bool {
	var v *tls.CertificateVerificationError
	return errors.As(err, &v)
}

func trustState(t *testing.T, url string) *State {
	t.Helper()
	st, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.Config.ServerURL = url + "/fog"
	return st
}

// A pinned CA that issued the server certificate verifies it.
func TestCheckTrustPinnedCAThatIssuedTheServer(t *testing.T) {
	srv, caPEM := tlsServer(t)
	st := trustState(t, srv.URL)
	if err := st.SaveCA(caPEM); err != nil {
		t.Fatal(err)
	}
	if err := st.CheckTrust(context.Background()); err != nil {
		t.Fatalf("a CA that issued the server did not verify it: %v", err)
	}
}

// A pinned CA from an earlier server fails with a verification error, the
// one setup treats as stale trust.
func TestCheckTrustPinnedUnrelatedCA(t *testing.T) {
	srv, _ := tlsServer(t)
	other, _, _, _, _ := testCA(t)
	st := trustState(t, srv.URL)
	if err := st.SaveCA(other); err != nil {
		t.Fatal(err)
	}
	err := st.CheckTrust(context.Background())
	if err == nil || !isVerifyError(err) {
		t.Fatalf("want a certificate verification error, got %v", err)
	}
}

// A server that does not answer is not a verification failure.
func TestCheckTrustUnreachableIsNotAVerificationError(t *testing.T) {
	srv, caPEM := tlsServer(t)
	st := trustState(t, srv.URL)
	if err := st.SaveCA(caPEM); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	err := st.CheckTrust(context.Background())
	if err == nil || isVerifyError(err) {
		t.Fatalf("want a dial error that is not a verification error, got %v", err)
	}
}

// A stored file with no certificate in it cannot verify anything.
func TestCheckTrustCAFileWithoutACertificate(t *testing.T) {
	srv, _ := tlsServer(t)
	st := trustState(t, srv.URL)
	if err := st.SaveCA([]byte("not a certificate")); err != nil {
		t.Fatal(err)
	}
	if err := st.CheckTrust(context.Background()); !errors.Is(err, ErrNoCACertificate) {
		t.Fatalf("want ErrNoCACertificate, got %v", err)
	}
}

// System trust verifies against the machine's store, and fails when the
// issuer is not in it.
func TestCheckTrustSystemTrust(t *testing.T) {
	srv, caPEM := tlsServer(t)
	st := trustState(t, srv.URL)
	if err := st.UseSystemTrust(); err != nil {
		t.Fatal(err)
	}
	if err := st.CheckTrust(context.Background()); err == nil || !isVerifyError(err) {
		t.Fatalf("want a verification error before the issuer is trusted, got %v", err)
	}
	trustAsSystem(t, caPEM)
	if err := st.CheckTrust(context.Background()); err != nil {
		t.Fatalf("a server on a machine-trusted issuer did not verify: %v", err)
	}
}

// ForgetTrust removes both kinds of settled trust and keeps the file state
// consistent on disk.
func TestForgetTrust(t *testing.T) {
	caPEM, _, _, _, _ := testCA(t)
	st := trustState(t, "https://127.0.0.1")
	if err := st.SaveCA(caPEM); err != nil {
		t.Fatal(err)
	}
	if err := st.ForgetTrust(); err != nil {
		t.Fatal(err)
	}
	if st.HasTrust() {
		t.Fatal("trust survived ForgetTrust")
	}
	if err := st.UseSystemTrust(); err != nil {
		t.Fatal(err)
	}
	if err := st.ForgetTrust(); err != nil {
		t.Fatal(err)
	}
	re, err := Load(st.Dir)
	if err != nil || re.HasTrust() {
		t.Fatalf("system trust survived ForgetTrust on disk: %v", err)
	}
}
