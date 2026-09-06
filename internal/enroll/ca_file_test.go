package enroll

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The FOG server publishes its CA twice, as management/other/ca.cert.pem
// and management/other/ca.cert.der. Somebody who saves the .der one in a
// browser and hands it to the installer used to get "CA bundle contains no
// certificates" out of NewClient, which names neither the file nor the
// format.
func TestReadCABundleAcceptsDER(t *testing.T) {
	caPEM, _, _, caCert, _ := testCA(t)

	dir := t.TempDir()
	derPath := filepath.Join(dir, "ca.cert.der")
	if err := os.WriteFile(derPath, caCert.Raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCABundle(derPath)
	if err != nil {
		t.Fatalf("DER refused: %v", err)
	}
	if _, err := NewClient("https://example.invalid/fog", got); err != nil {
		t.Fatalf("the converted bundle is not usable: %v", err)
	}

	pemPath := filepath.Join(dir, "ca.cert.pem")
	if err := os.WriteFile(pemPath, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadCABundle(pemPath); err != nil || string(got) != string(caPEM) {
		t.Fatalf("PEM should pass through unchanged: %v", err)
	}
}

// The wrong file entirely -- a private key, an HTML error page a proxy
// returned -- has to say what was expected, since this runs at install
// time where nobody is watching a log.
func TestReadCABundleRejectsNonCertificate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notacert.txt")
	if err := os.WriteFile(path, []byte("<html>404</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadCABundle(path)
	if err == nil {
		t.Fatal("a file with no certificate in it was accepted")
	}
	if !strings.Contains(err.Error(), "ca.cert.pem") {
		t.Fatalf("the error does not name where to get the right file: %v", err)
	}
}
