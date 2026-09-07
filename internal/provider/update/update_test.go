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
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FOGProject/fog-agent/internal/provider"
	"github.com/FOGProject/fog-agent/internal/release"
)

func TestValid(t *testing.T) {
	for _, v := range []string{"0.1.1", "v1.2.3", "1.0.0-rc1", "1.0.0+build7", "10.20.30"} {
		if !Valid(v) {
			t.Errorf("%q is a version this project can cut, and was refused", v)
		}
	}
	// "latest" is the one that matters: accepting it would hand the
	// decision to whatever central published this morning.
	for _, v := range []string{"", "latest", "1.2", "1.2.3.4", "1.2.x", "one.two.three", "1.2.3-"} {
		if Valid(v) {
			t.Errorf("%q must be refused, and was accepted", v)
		}
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.1", "0.1.1", 0},
		{"v0.1.1", "0.1.1", 0},
		{"0.1.1", "0.1.2", -1},
		{"0.2.0", "0.1.9", 1},
		{"1.0.0", "0.9.9", 1},
		{"0.1.10", "0.1.9", 1}, // not a string compare
		{"1.0.0-rc1", "1.0.0", -1},
		{"1.0.0-rc1", "1.0.0-rc2", -1},
		{"1.0.0+a", "1.0.0+b", 0}, // build metadata does not order
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := Compare(c.b, c.a); got != -c.want {
			t.Errorf("Compare(%q,%q) = %d, want %d (not symmetric)", c.b, c.a, got, -c.want)
		}
	}
}

// lab is a whole fake installation: a binary on disk, a state directory,
// a signing CA, and a server handing out a signed manifest. Every test
// below changes exactly one thing about it.
type lab struct {
	t        *testing.T
	dir      string // "program files"
	state    string
	exe      string
	rootPool *x509.CertPool
	leafKey  *ecdsa.PrivateKey
	leafPEM  string
	srv      *httptest.Server
	body     []byte
	env      []byte
	artifact []byte
	// tamper is applied to the manifest bytes just before they are served.
	tamper func([]byte) []byte
	// serveArtifact overrides the artifact bytes served.
	serveArtifact []byte
}

const (
	oldBinary = "I am the agent that is running\n"
	newBinary = "I am the agent that should be running\n"
)

func newLab(t *testing.T) *lab {
	t.Helper()
	l := &lab{t: t, dir: t.TempDir(), state: t.TempDir()}
	l.exe = filepath.Join(l.dir, "fog-agent")
	if err := os.WriteFile(l.exe, []byte(oldBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	// A config to be preserved across the update, so the revert test can
	// prove the enrollment survives.
	if err := os.WriteFile(filepath.Join(l.state, "config.json"),
		[]byte(`{"server_url":"https://fog.example.invalid/fog","host_id":231}`), 0o600); err != nil {
		t.Fatal(err)
	}
	l.artifact = []byte(newBinary)

	rootKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rootTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "lab signing root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	rootDER, _ := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	rootCert, _ := x509.ParseCertificate(rootDER)
	l.rootPool = x509.NewCertPool()
	l.rootPool.AddCert(rootCert)

	l.leafKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "lab release signing"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}
	leafDER, _ := x509.CreateCertificate(rand.Reader, leafTmpl, rootCert, &l.leafKey.PublicKey, rootKey)
	l.leafPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}))

	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/manifest.json"):
			body := l.body
			if l.tamper != nil {
				body = l.tamper(append([]byte{}, body...))
			}
			w.Write(body)
		case strings.HasSuffix(r.URL.Path, "/manifest.json.sig"):
			w.Write(l.env)
		case strings.HasSuffix(r.URL.Path, "/fog-agent-linux-amd64"):
			if l.serveArtifact != nil {
				w.Write(l.serveArtifact)
				return
			}
			w.Write(l.artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(l.srv.Close)
	l.publish(47, "0.4.2")
	return l
}

// publish signs a manifest describing the current artifact bytes.
func (l *lab) publish(seq int64, version string) {
	l.t.Helper()
	sum := sha256.Sum256(l.artifact)
	m := release.Manifest{
		Channel: "stable", Sequence: seq, Expires: time.Now().Add(48 * time.Hour),
		Versions: map[string]release.Version{version: {Artifacts: []release.Artifact{
			{OS: "linux", Arch: "amd64", SHA256: hex.EncodeToString(sum[:]),
				Size: int64(len(l.artifact)), URL: l.srv.URL + "/fog-agent-linux-amd64"},
		}}},
	}
	body, err := json.Marshal(m)
	if err != nil {
		l.t.Fatal(err)
	}
	l.body = body
	d := sha256.Sum256(body)
	sig, err := ecdsa.SignASN1(rand.Reader, l.leafKey, d[:])
	if err != nil {
		l.t.Fatal(err)
	}
	env, _ := json.Marshal(release.Envelope{Chain: []string{l.leafPEM},
		Alg: release.AlgECDSAP256SHA256, Sig: base64.StdEncoding.EncodeToString(sig)})
	l.env = env
}

func (l *lab) cfg() Config {
	return Config{
		Current: "0.1.1", GOOS: "linux", GOARCH: "amd64",
		Roots: l.rootPool, RootCount: 1,
		DefaultManifestURL: l.srv.URL + "/manifest.json",
		MinDowngrade:       "0.1.0",
		ExePath:            l.exe,
		StateDir:           l.state,
		// The real one, not a stand-in: the status check it does is
		// the thing the "cannot be fetched" case below is testing.
		Fetch:     HTTPFetch(http.DefaultClient),
		Now:       time.Now,
		Probation: 15 * time.Minute,
	}
}

func (l *lab) onDisk(path string) string {
	l.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		l.t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func TestRunAppliesAVerifiedUpdate(t *testing.T) {
	l := newLab(t)
	res, restart := Run(context.Background(), Desired{Version: "0.4.2"}, l.cfg())
	if res.Status != provider.StatusApplied || !restart {
		t.Fatalf("a verified update must apply and ask for a restart: %+v restart=%v", res, restart)
	}
	// The whole point: the file on disk is the new one, and the old one
	// is still there to go back to.
	if got := l.onDisk(l.exe); got != newBinary {
		t.Errorf("the binary was not replaced, it holds %q", got)
	}
	if got := l.onDisk(l.exe + ".prev"); got != oldBinary {
		t.Errorf("the previous binary was not kept, .prev holds %q", got)
	}
	p, err := LoadProbation(l.state)
	if err != nil || p == nil {
		t.Fatalf("probation record: %v %+v", err, p)
	}
	if p.From != "0.1.1" || p.To != "0.4.2" {
		t.Errorf("probation record does not describe the transition: %+v", p)
	}
	if p.Due(time.Now()) {
		t.Error("a probation armed just now must not already be due")
	}
	// The staged download must not be left behind.
	ents, _ := os.ReadDir(l.dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".fog-agent-update-") {
			t.Errorf("a staged download was left behind: %s", e.Name())
		}
	}
}

// Everything here must leave the binary exactly as it was. A refusal that
// half-replaces the agent is worse than no refusal.
func TestRunRefuses(t *testing.T) {
	cases := []struct {
		name    string
		desired Desired
		setup   func(*lab, *Config)
		want    string
	}{
		{
			name:    "a version that is not a version",
			desired: Desired{Version: "latest"}, want: DetailBadVersion,
		},
		{
			name:    "this build carries no signing root",
			desired: Desired{Version: "0.4.2"},
			setup:   func(l *lab, c *Config) { c.RootCount = 0; c.Roots = x509.NewCertPool() },
			want:    DetailNoSigningRoot,
		},
		{
			name:    "a downgrade below the floor",
			desired: Desired{Version: "0.0.9"},
			setup:   func(l *lab, c *Config) { c.MinDowngrade = "0.1.0" },
			want:    DetailBelowFloor,
		},
		{
			name:    "the manifest was changed after signing",
			desired: Desired{Version: "0.4.2"},
			setup: func(l *lab, c *Config) {
				l.tamper = func(b []byte) []byte {
					// Point the same signed manifest at different bytes.
					return []byte(strings.Replace(string(b), `"sequence":47`, `"sequence":48`, 1))
				}
			},
			want: DetailSignature,
		},
		{
			name:    "signed by a root this build does not carry",
			desired: Desired{Version: "0.4.2"},
			setup: func(l *lab, c *Config) {
				other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				tmpl := &x509.Certificate{SerialNumber: big.NewInt(9),
					NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
					BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign}
				der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &other.PublicKey, other)
				cert, _ := x509.ParseCertificate(der)
				pool := x509.NewCertPool()
				pool.AddCert(cert)
				c.Roots = pool
			},
			want: DetailSignature,
		},
		{
			name:    "the mirror serves bytes the manifest does not describe",
			desired: Desired{Version: "0.4.2"},
			setup: func(l *lab, c *Config) {
				// Same length, different content: the size check alone
				// would let this through, which is why the hash exists.
				l.serveArtifact = []byte(strings.Repeat("x", len(newBinary)))
			},
			want: DetailHash,
		},
		{
			name:    "a manifest older than one already accepted",
			desired: Desired{Version: "0.4.2"},
			setup: func(l *lab, c *Config) {
				if err := SaveSequence(l.state, 99); err != nil {
					l.t.Fatal(err)
				}
			},
			want: DetailStale,
		},
		{
			name:    "the version is not built for this platform",
			desired: Desired{Version: "0.4.2"},
			setup:   func(l *lab, c *Config) { c.GOARCH = "riscv64" },
			want:    DetailNoArtifact,
		},
		{
			name:    "the manifest cannot be fetched",
			desired: Desired{Version: "0.4.2"},
			setup:   func(l *lab, c *Config) { c.DefaultManifestURL = l.srv.URL + "/nope.json" },
			want:    DetailFetch,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)
			cfg := l.cfg()
			if tc.setup != nil {
				tc.setup(l, &cfg)
			}
			res, restart := Run(context.Background(), tc.desired, cfg)
			if res.Status != provider.StatusFailed {
				t.Fatalf("must fail, got %+v", res)
			}
			if restart {
				t.Fatal("a refusal must never ask for a restart")
			}
			if !strings.Contains(res.Detail, tc.want) {
				t.Errorf("detail %q does not name %q", res.Detail, tc.want)
			}
			// The agent keeps running the version it had.
			if got := l.onDisk(l.exe); got != oldBinary {
				t.Errorf("the running binary was touched: %q", got)
			}
			if p, _ := LoadProbation(l.state); p != nil {
				t.Errorf("a refusal armed a probation: %+v", p)
			}
		})
	}
}

func TestRunDoesNothingWhenAlreadyAtTheDesiredVersion(t *testing.T) {
	l := newLab(t)
	res, restart := Run(context.Background(), Desired{Version: "0.1.1"}, l.cfg())
	if res.Status != provider.StatusUnchanged || restart {
		t.Fatalf("got %+v restart=%v", res, restart)
	}
}

func TestRunDefersWhileSomethingIsInFlight(t *testing.T) {
	l := newLab(t)
	cfg := l.cfg()
	cfg.Busy = func() (string, bool) { return "snapin 41", true }
	res, restart := Run(context.Background(), Desired{Version: "0.4.2"}, cfg)
	if res.Status != provider.StatusUnchanged || restart {
		t.Fatalf("an update must wait for a snapin, not fail and not proceed: %+v", res)
	}
	if !strings.Contains(res.Detail, "snapin 41") {
		t.Errorf("the detail must name what it is waiting for, got %q", res.Detail)
	}
	if got := l.onDisk(l.exe); got != oldBinary {
		t.Error("the binary was replaced while a snapin was running")
	}
}

// A downgrade is the only fleet-wide recovery from a build that runs and
// polls perfectly well and behaves badly, so it has to work.
func TestRunAcceptsADowngradeAboveTheFloor(t *testing.T) {
	l := newLab(t)
	l.publish(48, "0.1.0")
	cfg := l.cfg()
	cfg.Current = "0.4.2"
	cfg.MinDowngrade = "0.1.0"
	res, restart := Run(context.Background(), Desired{Version: "0.1.0"}, cfg)
	if res.Status != provider.StatusApplied || !restart {
		t.Fatalf("a downgrade to the floor must be allowed: %+v", res)
	}
}

func TestRevertPutsTheBinaryAndConfigBack(t *testing.T) {
	l := newLab(t)
	cfg := l.cfg()
	before := l.onDisk(filepath.Join(l.state, "config.json"))

	if res, _ := Run(context.Background(), Desired{Version: "0.4.2"}, cfg); res.Status != provider.StatusApplied {
		t.Fatalf("setup: %+v", res)
	}
	// The new binary writes state the old one would not have written --
	// the case a revert has to survive.
	if err := os.WriteFile(filepath.Join(l.state, "config.json"),
		[]byte(`{"server_url":"https://fog.example.invalid/fog","host_id":231,"something_new":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := Revert(cfg, "test")
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if p.To != "0.4.2" || p.From != "0.1.1" {
		t.Errorf("revert did not report the transition it undid: %+v", p)
	}
	if got := l.onDisk(l.exe); got != oldBinary {
		t.Errorf("the previous binary is not back, disk holds %q", got)
	}
	if got := l.onDisk(l.exe + ".bad"); got != newBinary {
		t.Errorf(".bad must keep the binary that failed, holds %q", got)
	}
	if got := l.onDisk(filepath.Join(l.state, "config.json")); got != before {
		t.Errorf("the config was not restored:\n got %s\nwant %s", got, before)
	}
	if p, _ := LoadProbation(l.state); p != nil {
		t.Error("the probation record survived a revert")
	}
	// And a second revert has nothing to do rather than doing damage.
	if _, err := Revert(cfg, "test"); err == nil {
		t.Error("reverting twice must not silently do something")
	}
	if got := l.onDisk(l.exe); got != oldBinary {
		t.Errorf("the second revert changed the binary: %q", got)
	}
}

func TestSequenceFloorOnlyRises(t *testing.T) {
	dir := t.TempDir()
	if n, err := LoadSequence(dir); n != 0 || err != nil {
		t.Fatalf("a fresh agent starts at zero, got %d %v", n, err)
	}
	if err := SaveSequence(dir, 47); err != nil {
		t.Fatal(err)
	}
	if err := SaveSequence(dir, 12); err != nil {
		t.Fatal(err)
	}
	if n, _ := LoadSequence(dir); n != 47 {
		t.Errorf("the floor went down to %d", n)
	}
}

// The ordering rule: an update that cannot arm its own rollback must not
// happen. Everything else about the update is made to succeed here, and
// only the probation record is made impossible to write -- a directory
// standing where the file goes. An earlier version of this test pointed
// the whole state directory at a missing path, which failed one step
// sooner and would have passed with the arming removed entirely.
func TestAnUpdateThatCannotArmItsRollbackDoesNotHappen(t *testing.T) {
	l := newLab(t)
	cfg := l.cfg()
	if err := os.Mkdir(filepath.Join(l.state, probationFile), 0o700); err != nil {
		t.Fatal(err)
	}
	res, restart := Run(context.Background(), Desired{Version: "0.4.2"}, cfg)
	if res.Status != provider.StatusFailed || restart {
		t.Fatalf("an update that cannot arm its own rollback must not happen: %+v", res)
	}
	if !strings.Contains(res.Detail, DetailCannotArm) {
		t.Errorf("detail %q does not name %q", res.Detail, DetailCannotArm)
	}
	// The sequence floor did rise, which is correct and is what makes
	// this test prove the arming step rather than an earlier one.
	if n, _ := LoadSequence(l.state); n != 47 {
		t.Fatalf("this test only gates the arming step if everything before it worked; sequence is %d", n)
	}
	if got := l.onDisk(l.exe); got != oldBinary {
		t.Error("the binary was replaced despite the rollback not being armed")
	}
}
