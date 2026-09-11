package update

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FOGProject/fog-agent/internal/provider"
	"github.com/FOGProject/fog-agent/internal/release"
)

const originArtifact = "/fog-agent-linux-amd64"

// counting wraps the lab's origin server so a test can say what it served.
// refuse names paths it answers 404 for, as an origin a host cannot reach.
func counting(l *lab, refuse ...string) func(path string) int {
	var mu sync.Mutex
	hits := map[string]int{}
	orig := l.srv.Config.Handler
	l.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		for _, p := range refuse {
			if r.URL.Path == p {
				http.NotFound(w, r)
				return
			}
		}
		orig.ServeHTTP(w, r)
	})
	return func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[path]
	}
}

// fogServer stands in for the FOG server's payload route.
type fogServer struct {
	srv    *httptest.Server
	status int
	body   []byte
	mu     sync.Mutex
	asked  []string
}

func newFogServer(t *testing.T, status int, body []byte) *fogServer {
	t.Helper()
	f := &fogServer{status: status, body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.asked = append(f.asked, r.URL.Path)
		f.mu.Unlock()
		w.WriteHeader(f.status)
		w.Write(f.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// payload does what enroll.Client.Payload does, against this server: the
// same route and the same refusal of anything but 200. The real client
// cannot be used here, because enroll imports this package.
func (f *fogServer) payload(ctx context.Context, id int, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/agent/v1/payload/update/%d", f.srv.URL, id), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update payload: HTTP %d", resp.StatusCode)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (f *fogServer) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// inline adds the lab's signed manifest and envelope the way a syncing
// server sends them.
func (l *lab) inline(d Desired) Desired {
	d.Manifest = base64.StdEncoding.EncodeToString(l.body)
	d.Signature = base64.StdEncoding.EncodeToString(l.env)
	return d
}

func TestRunVerifiesTheManifestTheServerSent(t *testing.T) {
	l := newLab(t)
	hits := counting(l)
	res, restart := Run(context.Background(), l.inline(Desired{Version: "0.4.2"}), l.cfg())
	if res.Status != provider.StatusApplied || !restart {
		t.Fatalf("a verified inline manifest must apply: %+v restart=%v", res, restart)
	}
	if n := hits("/manifest.json") + hits("/manifest.json.sig"); n != 0 {
		t.Errorf("the manifest URL was fetched %d times with a usable pair in hand", n)
	}
	if got := l.onDisk(l.exe); got != newBinary {
		t.Errorf("the binary was not replaced, it holds %q", got)
	}
}

// served signs the lab's manifest, edited by change, with key under the
// leaf certificate leafPEM, and puts the pair in d as a server sends it.
func (l *lab) served(d Desired, key *ecdsa.PrivateKey, leafPEM string, change func(*release.Manifest)) Desired {
	l.t.Helper()
	var m release.Manifest
	if err := json.Unmarshal(l.body, &m); err != nil {
		l.t.Fatal(err)
	}
	change(&m)
	body, err := json.Marshal(m)
	if err != nil {
		l.t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		l.t.Fatal(err)
	}
	env, _ := json.Marshal(release.Envelope{Chain: []string{leafPEM},
		Alg: release.AlgECDSAP256SHA256, Sig: base64.StdEncoding.EncodeToString(sig)})
	d.Manifest = base64.StdEncoding.EncodeToString(body)
	d.Signature = base64.StdEncoding.EncodeToString(env)
	return d
}

// stranger is a code signing key and leaf under a root this build does not
// carry.
func stranger(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	rootKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rootTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "someone else's root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	rootDER, _ := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	rootCert, _ := x509.ParseCertificate(rootDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(10), Subject: pkix.Name{CommonName: "someone else's release signing"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}
	leafDER, _ := x509.CreateCertificate(rand.Reader, leafTmpl, rootCert, &key.PublicKey, rootKey)
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}))
}

func expired(m *release.Manifest) { m.Expires = time.Now().Add(-time.Hour) }

func tampered(l *lab, d Desired) Desired {
	d = l.inline(d)
	d.Manifest = base64.StdEncoding.EncodeToString(
		[]byte(strings.Replace(string(l.body), `"sequence":47`, `"sequence":48`, 1)))
	return d
}

// A server whose release sync stopped keeps sending the copy it holds. That
// copy expires, or falls behind a sequence the host saw elsewhere, while the
// origin is healthy. The pair gets no trust a download would not get, and the
// URL's copy goes through every check the pair failed.
func TestRunFallsBackToTheURLWhenTheServersManifestIsRefused(t *testing.T) {
	cases := []struct {
		name string
		pair func(*lab, Desired) Desired
	}{
		{"the server's manifest has expired", func(l *lab, d Desired) Desired {
			return l.served(d, l.leafKey, l.leafPEM, expired)
		}},
		{"the server's manifest is signed by a key this build does not trust", func(l *lab, d Desired) Desired {
			key, leaf := stranger(l.t)
			return l.served(d, key, leaf, func(*release.Manifest) {})
		}},
		{"the server's manifest was changed after signing", tampered},
		{"the server's manifest is older than one this host accepted", func(l *lab, d Desired) Desired {
			if err := SaveSequence(l.state, 45); err != nil {
				l.t.Fatal(err)
			}
			return l.served(d, l.leafKey, l.leafPEM, func(m *release.Manifest) { m.Sequence = 40 })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)
			hits := counting(l)
			// The URL the server names, not the compiled one, is the one
			// asked.
			cfg := l.cfg()
			cfg.DefaultManifestURL = l.srv.URL + "/nope.json"
			d := tc.pair(l, Desired{Version: "0.4.2", ManifestURL: l.srv.URL + "/manifest.json"})
			res, restart := Run(context.Background(), d, cfg)
			if res.Status != provider.StatusApplied || !restart {
				t.Fatalf("a refused inline manifest must fall back to a good URL copy: %+v", res)
			}
			if hits("/manifest.json") != 1 || hits("/manifest.json.sig") != 1 {
				t.Errorf("want the manifest URL asked once, got manifest %d, signature %d",
					hits("/manifest.json"), hits("/manifest.json.sig"))
			}
			if got := l.onDisk(l.exe); got != newBinary {
				t.Errorf("the binary was not replaced, it holds %q", got)
			}
		})
	}
}

func TestRunNamesBothManifestsWhenNeitherIsAccepted(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*lab, *Config)
		want  string
	}{
		{"the URL copy is refused too", func(l *lab, c *Config) {
			l.tamper = func(b []byte) []byte {
				return []byte(strings.Replace(string(b), `"sequence":47`, `"sequence":48`, 1))
			}
		}, DetailSignature},
		{"the URL cannot be fetched", func(l *lab, c *Config) {
			c.DefaultManifestURL = l.srv.URL + "/nope.json"
		}, DetailFetch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)
			cfg := l.cfg()
			tc.setup(l, &cfg)
			d := l.served(Desired{Version: "0.4.2"}, l.leafKey, l.leafPEM, expired)
			res, restart := Run(context.Background(), d, cfg)
			if res.Status != provider.StatusFailed || restart {
				t.Fatalf("must fail: %+v", res)
			}
			// The URL attempt's code leads, then each attempt's own reason.
			if lead := tc.want + ": server copy: " + DetailStale + ":"; !strings.HasPrefix(res.Detail, lead) {
				t.Errorf("detail %q does not lead with %q", res.Detail, lead)
			}
			if !strings.Contains(res.Detail, "; origin: "+tc.want+":") {
				t.Errorf("detail %q does not name the URL attempt's %s", res.Detail, tc.want)
			}
			if got := l.onDisk(l.exe); got != oldBinary {
				t.Errorf("the running binary was touched: %q", got)
			}
		})
	}
}

// With nowhere to fall back to, the refusal of the pair is the whole story,
// and it reads exactly as it did before there was a fallback.
func TestRunReportsARefusedPairAsItIsWhenThereIsNoURL(t *testing.T) {
	l := newLab(t)
	cfg := l.cfg()
	cfg.DefaultManifestURL = ""
	d := l.served(Desired{Version: "0.4.2"}, l.leafKey, l.leafPEM, expired)
	res, restart := Run(context.Background(), d, cfg)
	if res.Status != provider.StatusFailed || restart {
		t.Fatalf("must fail: %+v", res)
	}
	if want := DetailStale + ": the manifest has expired"; res.Detail != want {
		t.Errorf("detail %q, want %q", res.Detail, want)
	}
}

func TestRunIgnoresAnIncompleteOrUnreadablePair(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Desired)
	}{
		{"a manifest with no signature", func(d *Desired) { d.Signature = "" }},
		{"a signature with no manifest", func(d *Desired) { d.Manifest = "" }},
		{"a manifest that is not base64", func(d *Desired) { d.Manifest = "not*base64!" }},
		{"a signature that is not base64", func(d *Desired) { d.Signature = "not*base64!" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)
			hits := counting(l)
			d := l.inline(Desired{Version: "0.4.2"})
			tc.edit(&d)
			res, restart := Run(context.Background(), d, l.cfg())
			if res.Status != provider.StatusApplied || !restart {
				t.Fatalf("an unusable pair must be ignored and the URL used: %+v", res)
			}
			if hits("/manifest.json") != 1 || hits("/manifest.json.sig") != 1 {
				t.Errorf("the URL was not asked: manifest %d, signature %d",
					hits("/manifest.json"), hits("/manifest.json.sig"))
			}
		})
	}
}

func TestRunTakesTheServerCopyWithoutTouchingTheOrigin(t *testing.T) {
	l := newLab(t)
	hits := counting(l)
	// An origin serving wrong bytes, so a fallback that should not have
	// happened fails the update rather than passing unnoticed.
	l.serveArtifact = []byte(strings.Repeat("x", len(newBinary)))
	f := newFogServer(t, http.StatusOK, []byte(newBinary))
	cfg := l.cfg()
	cfg.Payload = f.payload
	res, restart := Run(context.Background(), Desired{Version: "0.4.2", Artifact: 7}, cfg)
	if res.Status != provider.StatusApplied || !restart {
		t.Fatalf("a good server copy must apply: %+v", res)
	}
	if n := hits(originArtifact); n != 0 {
		t.Errorf("the origin was fetched %d times after a good server copy", n)
	}
	if got := f.calls(); len(got) != 1 || got[0] != "/agent/v1/payload/update/7" {
		t.Errorf("the server was asked %v", got)
	}
	if got := l.onDisk(l.exe); got != newBinary {
		t.Errorf("the binary was not replaced, it holds %q", got)
	}
}

func TestRunFallsBackToTheOriginWhenTheServerCopyFails(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"the server answers 500", http.StatusInternalServerError, "no"},
		{"the server serves different bytes", http.StatusOK, strings.Repeat("x", len(newBinary))},
		{"the server serves too few bytes", http.StatusOK, newBinary[:5]},
		// Past the declared size, so the copy stops reading mid-stream and
		// the abandoned download must not hang the fallback.
		{"the server serves too many bytes", http.StatusOK, strings.Repeat(newBinary, 5000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)
			hits := counting(l)
			f := newFogServer(t, tc.status, []byte(tc.body))
			cfg := l.cfg()
			cfg.Payload = f.payload
			res, restart := Run(context.Background(), Desired{Version: "0.4.2", Artifact: 7}, cfg)
			if res.Status != provider.StatusApplied || !restart {
				t.Fatalf("a failed server copy must fall back to the origin: %+v", res)
			}
			if len(f.calls()) != 1 || hits(originArtifact) != 1 {
				t.Errorf("want one server attempt and one origin fetch, got %d and %d",
					len(f.calls()), hits(originArtifact))
			}
			if got := l.onDisk(l.exe); got != newBinary {
				t.Errorf("the binary was not replaced, it holds %q", got)
			}
		})
	}
}

func TestRunNamesBothSourcesWhenNeitherDelivers(t *testing.T) {
	wrong := strings.Repeat("x", len(newBinary))
	cases := []struct {
		name         string
		serverStatus int
		serverBody   string
		originWrong  bool // origin serves wrong bytes; otherwise it answers 404
		want         string
	}{
		{"both serve wrong bytes", http.StatusOK, wrong, true, DetailHash + ":"},
		{"neither can be reached", http.StatusServiceUnavailable, "", false, DetailFetch + ":"},
		// Only one source said anything about the bytes. The other could
		// not deliver, so this is a machine that did not get there.
		{"wrong bytes from the server, no origin", http.StatusOK, wrong, false, DetailFetch + ":"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)
			if tc.originWrong {
				l.serveArtifact = []byte(wrong)
				counting(l)
			} else {
				counting(l, originArtifact)
			}
			f := newFogServer(t, tc.serverStatus, []byte(tc.serverBody))
			cfg := l.cfg()
			cfg.Payload = f.payload
			res, restart := Run(context.Background(), Desired{Version: "0.4.2", Artifact: 7}, cfg)
			if res.Status != provider.StatusFailed || restart {
				t.Fatalf("must fail: %+v", res)
			}
			if !strings.HasPrefix(res.Detail, tc.want) {
				t.Errorf("detail %q does not lead with %q", res.Detail, tc.want)
			}
			if !strings.Contains(res.Detail, "server") || !strings.Contains(res.Detail, "origin") {
				t.Errorf("detail %q does not name both attempts", res.Detail)
			}
			if got := l.onDisk(l.exe); got != oldBinary {
				t.Errorf("the running binary was touched: %q", got)
			}
		})
	}
}

// A server that sends none of the new fields gets exactly the old path,
// even on an agent that has a way to ask its server for payloads.
func TestRunWithoutTheNewFieldsTakesTheOriginalPath(t *testing.T) {
	l := newLab(t)
	hits := counting(l)
	f := newFogServer(t, http.StatusOK, []byte(newBinary))
	cfg := l.cfg()
	cfg.Payload = f.payload
	res, restart := Run(context.Background(), Desired{Version: "0.4.2"}, cfg)
	if res.Status != provider.StatusApplied || !restart {
		t.Fatalf("the original path must still apply: %+v", res)
	}
	if got := f.calls(); len(got) != 0 {
		t.Errorf("the server was asked for a payload it never offered: %v", got)
	}
	if hits("/manifest.json") != 1 || hits("/manifest.json.sig") != 1 || hits(originArtifact) != 1 {
		t.Errorf("want manifest, signature and artifact fetched once each, got %d %d %d",
			hits("/manifest.json"), hits("/manifest.json.sig"), hits(originArtifact))
	}
}
