package release

import (
	"bytes"
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
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// The fixtures are minted here rather than checked in as files: a test
// double for a signature has to come from a real signer, and a real
// signer is twenty lines of x509. Checked-in PEM would also expire.

type ca struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func newRoot(t *testing.T, notBefore, notAfter time.Time) *ca {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "FOG Agent Signing CA (test)"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &ca{cert: c, key: key, pem: pemOf(der)}
}

// newLeaf issues a leaf under root with the given extended key usages, so
// a test can ask for one that is not a code signing certificate.
func newLeaf(t *testing.T, root *ca, eku []x509.ExtKeyUsage, notBefore, notAfter time.Time, curve elliptic.Curve) *ca {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "fog-agent release signing (test)"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           eku,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, root.cert, &key.PublicKey, root.key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &ca{cert: c, key: key, pem: pemOf(der)}
}

func pemOf(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func sign(t *testing.T, leaf *ca, body []byte) *Envelope {
	t.Helper()
	sum := sha256.Sum256(body)
	sig, err := ecdsa.SignASN1(rand.Reader, leaf.key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return &Envelope{
		Chain: []string{leaf.pem},
		Alg:   AlgECDSAP256SHA256,
		Sig:   base64.StdEncoding.EncodeToString(sig),
	}
}

func manifestBody(t *testing.T, expires time.Time) []byte {
	t.Helper()
	m := Manifest{
		Channel:  "stable",
		Sequence: 47,
		Expires:  expires,
		Versions: map[string]Version{
			"0.4.2": {Notes: "https://example.invalid/notes", Artifacts: []Artifact{
				{OS: "linux", Arch: "amd64", SHA256: strings.Repeat("ab", 32), Size: 12, URL: "https://example.invalid/a"},
				{OS: "windows", Arch: "amd64", SHA256: strings.Repeat("cd", 32), Size: 34, URL: "https://example.invalid/b"},
			}},
		},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rig is the ordinary case every failure test mutates one thing away from.
func rig(t *testing.T) (body []byte, env *Envelope, roots *x509.CertPool, now time.Time) {
	t.Helper()
	now = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	root := newRoot(t, now.Add(-time.Hour), now.Add(20*365*24*time.Hour))
	leaf := newLeaf(t, root, []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		now.Add(-time.Hour), now.Add(90*24*time.Hour), elliptic.P256())
	body = manifestBody(t, now.Add(90*24*time.Hour))
	env = sign(t, leaf, body)
	roots = x509.NewCertPool()
	roots.AddCert(root.cert)
	return
}

func TestVerifyAcceptsAGenuineManifest(t *testing.T) {
	body, env, roots, now := rig(t)
	m, err := Verify(body, env, roots, now)
	if err != nil {
		t.Fatalf("a manifest signed by a leaf under the compiled-in root must verify: %v", err)
	}
	if m.Sequence != 47 || m.Channel != "stable" {
		t.Fatalf("verified manifest did not round-trip: %+v", m)
	}
}

// Every case below must fail. A verifier that cannot be made to say no is
// not a verifier, so these are the point of the file.
func TestVerifyRefuses(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		// mutate returns the body, envelope and roots to try.
		mutate func(t *testing.T) ([]byte, *Envelope, *x509.CertPool)
		want   error
	}{
		{
			name: "one byte of the manifest changed after signing",
			mutate: func(t *testing.T) ([]byte, *Envelope, *x509.CertPool) {
				body, env, roots, _ := rig(t)
				// Swap a digit inside the sha256 of the linux artifact:
				// this is exactly the attack -- keep the manifest
				// otherwise intact and point it at other bytes.
				tampered := bytes.Replace(body, []byte(strings.Repeat("ab", 32)), []byte(strings.Repeat("ba", 32)), 1)
				if bytes.Equal(tampered, body) {
					t.Fatal("the tamper did not change anything, so this proves nothing")
				}
				return tampered, env, roots
			},
			want: ErrSignature,
		},
		{
			name: "signed by a leaf under a different root",
			mutate: func(t *testing.T) ([]byte, *Envelope, *x509.CertPool) {
				body, _, roots, _ := rig(t)
				other := newRoot(t, now.Add(-time.Hour), now.Add(time.Hour*24))
				leaf := newLeaf(t, other, []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
					now.Add(-time.Hour), now.Add(24*time.Hour), elliptic.P256())
				return body, sign(t, leaf, body), roots
			},
			want: ErrSignature,
		},
		{
			name: "leaf chains to the root but is not a code signing certificate",
			mutate: func(t *testing.T) ([]byte, *Envelope, *x509.CertPool) {
				now := now
				root := newRoot(t, now.Add(-time.Hour), now.Add(24*time.Hour))
				// A client certificate under the same CA. FOG's PKI
				// issues these by the hundred; none of them may sign a
				// release.
				leaf := newLeaf(t, root, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
					now.Add(-time.Hour), now.Add(24*time.Hour), elliptic.P256())
				body := manifestBody(t, now.Add(24*time.Hour))
				roots := x509.NewCertPool()
				roots.AddCert(root.cert)
				return body, sign(t, leaf, body), roots
			},
			want: ErrSignature,
		},
		{
			name: "leaf has expired",
			mutate: func(t *testing.T) ([]byte, *Envelope, *x509.CertPool) {
				root := newRoot(t, now.Add(-48*time.Hour), now.Add(24*time.Hour))
				leaf := newLeaf(t, root, []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
					now.Add(-48*time.Hour), now.Add(-time.Hour), elliptic.P256())
				body := manifestBody(t, now.Add(24*time.Hour))
				roots := x509.NewCertPool()
				roots.AddCert(root.cert)
				return body, sign(t, leaf, body), roots
			},
			want: ErrSignature,
		},
		{
			name: "leaf key is not P-256",
			mutate: func(t *testing.T) ([]byte, *Envelope, *x509.CertPool) {
				root := newRoot(t, now.Add(-time.Hour), now.Add(24*time.Hour))
				leaf := newLeaf(t, root, []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
					now.Add(-time.Hour), now.Add(24*time.Hour), elliptic.P384())
				body := manifestBody(t, now.Add(24*time.Hour))
				sum := sha256.Sum256(body)
				sig, err := ecdsa.SignASN1(rand.Reader, leaf.key, sum[:])
				if err != nil {
					t.Fatal(err)
				}
				roots := x509.NewCertPool()
				roots.AddCert(root.cert)
				return body, &Envelope{Chain: []string{leaf.pem}, Alg: AlgECDSAP256SHA256,
					Sig: base64.StdEncoding.EncodeToString(sig)}, roots
			},
			want: ErrSignature,
		},
		{
			name: "an algorithm this build does not know",
			mutate: func(t *testing.T) ([]byte, *Envelope, *x509.CertPool) {
				body, env, roots, _ := rig(t)
				env.Alg = "ed25519"
				return body, env, roots
			},
			want: ErrSignature,
		},
		{
			name: "no chain at all",
			mutate: func(t *testing.T) ([]byte, *Envelope, *x509.CertPool) {
				body, env, roots, _ := rig(t)
				env.Chain = nil
				return body, env, roots
			},
			want: ErrSignature,
		},
		{
			name: "the chain is not a certificate",
			mutate: func(t *testing.T) ([]byte, *Envelope, *x509.CertPool) {
				body, env, roots, _ := rig(t)
				env.Chain = []string{"-----BEGIN CERTIFICATE-----\nbm90IGEgY2VydA==\n-----END CERTIFICATE-----\n"}
				return body, env, roots
			},
			want: ErrSignature,
		},
		{
			name: "the signature is not base64",
			mutate: func(t *testing.T) ([]byte, *Envelope, *x509.CertPool) {
				body, env, roots, _ := rig(t)
				env.Sig = "not base64 !!"
				return body, env, roots
			},
			want: ErrSignature,
		},
		{
			name: "the manifest has expired",
			mutate: func(t *testing.T) ([]byte, *Envelope, *x509.CertPool) {
				root := newRoot(t, now.Add(-time.Hour), now.Add(24*time.Hour))
				leaf := newLeaf(t, root, []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
					now.Add(-time.Hour), now.Add(24*time.Hour), elliptic.P256())
				body := manifestBody(t, now.Add(-time.Minute))
				roots := x509.NewCertPool()
				roots.AddCert(root.cert)
				return body, sign(t, leaf, body), roots
			},
			want: ErrStale,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, env, roots := tc.mutate(t)
			m, err := Verify(body, env, roots, now)
			if err == nil {
				t.Fatalf("this must not verify, and it did: %+v", m)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("refused for the wrong reason: got %v, want %v", err, tc.want)
			}
		})
	}
}

// A signature over a nil envelope must not panic on the way to refusing.
func TestVerifyRefusesANilEnvelope(t *testing.T) {
	body, _, roots, now := rig(t)
	if _, err := Verify(body, nil, roots, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("got %v, want ErrSignature", err)
	}
}

func TestFreshRefusesAReplayedManifest(t *testing.T) {
	m := &Manifest{Sequence: 47}
	if err := Fresh(m, 47); err != nil {
		t.Errorf("the same sequence again is not a replay: %v", err)
	}
	if err := Fresh(m, 46); err != nil {
		t.Errorf("a newer manifest must be accepted: %v", err)
	}
	if err := Fresh(m, 48); !errors.Is(err, ErrStale) {
		t.Errorf("a manifest older than one already accepted must be refused, got %v", err)
	}
}

func TestFind(t *testing.T) {
	body, env, roots, now := rig(t)
	m, err := Verify(body, env, roots, now)
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.Find("0.4.2", "linux", "amd64")
	if err != nil {
		t.Fatalf("the platform in the manifest must be found: %v", err)
	}
	if a.Size != 12 {
		t.Errorf("wrong artifact: %+v", a)
	}
	if _, err := m.Find("0.4.2", "linux", "arm64"); !errors.Is(err, ErrNoArtifact) {
		t.Errorf("a platform with no build must be ErrNoArtifact, got %v", err)
	}
	if _, err := m.Find("9.9.9", "linux", "amd64"); !errors.Is(err, ErrNoArtifact) {
		t.Errorf("a version that was never released must be ErrNoArtifact, got %v", err)
	}
}

func TestCopyVerified(t *testing.T) {
	payload := []byte("a plausible binary")
	sum := sha256.Sum256(payload)
	good := &Artifact{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload))}

	t.Run("the declared bytes", func(t *testing.T) {
		var out bytes.Buffer
		if err := CopyVerified(&out, bytes.NewReader(payload), good); err != nil {
			t.Fatalf("the artifact the manifest describes must copy: %v", err)
		}
		if !bytes.Equal(out.Bytes(), payload) {
			t.Error("the bytes written are not the bytes read")
		}
	})

	t.Run("upper case in the manifest digest", func(t *testing.T) {
		a := *good
		a.SHA256 = strings.ToUpper(a.SHA256)
		var out bytes.Buffer
		if err := CopyVerified(&out, bytes.NewReader(payload), &a); err != nil {
			t.Fatalf("a hand-written digest with capitals must still match: %v", err)
		}
	})

	t.Run("different bytes, same length", func(t *testing.T) {
		other := []byte("a malicious binary")
		if len(other) != len(payload) {
			t.Fatal("this case only means something if the lengths match")
		}
		var out bytes.Buffer
		if err := CopyVerified(&out, bytes.NewReader(other), good); !errors.Is(err, ErrHash) {
			t.Fatalf("got %v, want ErrHash", err)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		var out bytes.Buffer
		if err := CopyVerified(&out, bytes.NewReader(payload[:5]), good); !errors.Is(err, ErrHash) {
			t.Fatalf("got %v, want ErrHash", err)
		}
	})

	t.Run("longer than declared", func(t *testing.T) {
		var out bytes.Buffer
		long := append(append([]byte{}, payload...), []byte(" and then some")...)
		if err := CopyVerified(&out, bytes.NewReader(long), good); !errors.Is(err, ErrHash) {
			t.Fatalf("got %v, want ErrHash", err)
		}
		// The stop is what matters: a mirror must not be able to make
		// the agent write an unbounded file.
		if int64(out.Len()) > good.Size+1 {
			t.Errorf("wrote %d bytes for a declared size of %d", out.Len(), good.Size)
		}
	})
}

// A build carrying no signing root must refuse everything. This is the
// state of a plain checkout, and it has to fail closed: an empty pool that
// verified anything would mean every unsigned build in the world trusts
// every manifest in the world.
func TestNoRootTrustsNothing(t *testing.T) {
	body, env, _, now := rig(t)
	if _, err := Verify(body, env, x509.NewCertPool(), now); !errors.Is(err, ErrSignature) {
		t.Fatalf("an empty root pool must refuse, got %v", err)
	}
}

// Whatever is in roots.pem must be usable. Deliberately not asserting that
// it is empty: a release build replaces the file before compiling, and a
// test that fails on a real release is a test that gets deleted.
func TestCompiledInRootsAreSane(t *testing.T) {
	pool, n := Roots()
	if pool == nil {
		t.Fatal("Roots must always return a pool, even an empty one")
	}
	for _, c := range parsePEMCerts(rootsPEM) {
		if !c.IsCA {
			t.Errorf("roots.pem carries %q, which is not a CA certificate", c.Subject.CommonName)
		}
	}
	if got := len(parsePEMCerts(rootsPEM)); got != n {
		t.Errorf("Roots counted %d certificates, the file has %d", n, got)
	}
	t.Logf("this build trusts %d signing root(s)", n)
}

// Comments and stray text around the blocks must not stop the file being
// read: it is concatenated by a script and edited by hand.
func TestRootsFileToleratesComments(t *testing.T) {
	root := newRoot(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	mixed := []byte("# a comment\n\nnot pem at all\n" + root.pem + "# trailing note\n")
	if got := len(parsePEMCerts(mixed)); got != 1 {
		t.Fatalf("expected to find the one certificate among the noise, found %d", got)
	}
}
