// Package release decides whether a self-update is allowed to run
// (design 0015 section 3).
//
// The one idea: the FOG server names a version, and nothing else. What
// that version *is* comes from a manifest signed by a certificate issued
// under a FOG-held signing CA whose root is compiled into this binary, so
// neither the server that asked for the update nor the mirror that served
// the bytes has to be trusted. A compromised server can pick which
// published version a fleet runs; it cannot publish one.
//
// Verification is the standard library and nothing else: x509 for the
// chain, ecdsa for the signature, sha256 for the artifact. The envelope is
// a small JSON object rather than PKCS#7 because CMS is not in the
// standard library and the whole point of this shape is that checking a
// signature costs the agent no dependency.
package release

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"time"
)

// AlgECDSAP256SHA256 is the only signature algorithm this build accepts.
// Named on the wire so a future one is a value and not a guess, and
// refused when it is anything else: an unrecognized algorithm must fail
// closed, never fall through to "try the one we know".
const AlgECDSAP256SHA256 = "ecdsa-p256-sha256"

// Why the errors are sentinels: the agent reports the failing check by
// name to the server (design 0015 section 3.6), because "update failed"
// on its own does not tell an admin whether a mirror is broken or someone
// is trying something.
var (
	// ErrSignature covers every way the chain or the signature is not
	// what it must be. Deliberately one error and not five: telling a
	// caller which part of a bad signature was bad is telling whoever
	// produced it how to get closer.
	ErrSignature = errors.New("release: manifest signature does not verify")
	// ErrStale is a manifest that has expired, or one older than the
	// newest this agent has already seen. A validly signed manifest
	// replayed to hold a fleet on a version with a known hole is the
	// obvious attack on a signed-hash scheme, and a signature alone
	// does not stop it.
	ErrStale = errors.New("release: manifest is stale")
	// ErrNoArtifact is a manifest that verifies and simply has nothing
	// for this version on this platform. Not a security failure.
	ErrNoArtifact = errors.New("release: no artifact for this version and platform")
	// ErrHash is a downloaded artifact whose bytes are not the ones the
	// manifest declared.
	ErrHash = errors.New("release: artifact hash does not match the manifest")
)

// Manifest is the signed description of what has been released. It is
// small on purpose: it is fetched by every agent that has been told to
// update, and it is the only thing the signing key ever signs.
type Manifest struct {
	Channel string `json:"channel"`
	// Sequence only ever increases. An agent refuses a manifest whose
	// sequence is below the highest it has already accepted, which is
	// what makes a replayed old manifest useless.
	Sequence int64 `json:"sequence"`
	// Expires bounds how long a mirror may keep serving a copy. Expiry
	// means "refuse to update, keep running, say so" -- never "stop".
	Expires  time.Time          `json:"expires"`
	Versions map[string]Version `json:"versions"`
}

// Version is one released version across every platform it was built for.
type Version struct {
	// Security marks a release that fixes a security issue. The server
	// surfaces it prominently; nothing applies it automatically, because
	// urgency is a reason to tell an admin, not a reason to take the
	// decision away from them (design 0015 section 11).
	Security  bool       `json:"security"`
	Notes     string     `json:"notes"`
	Artifacts []Artifact `json:"artifacts"`
}

// Artifact is one built file: the bytes, and what they must hash to.
type Artifact struct {
	OS   string `json:"os"`   // GOOS
	Arch string `json:"arch"` // GOARCH
	// SHA256 is hex, lower case. Compared case-insensitively anyway,
	// because a manifest written by hand is a manifest with a capital
	// letter in it.
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	URL    string `json:"url"`
}

// Envelope is the detached signature that travels beside a manifest.
// Chain is PEM, leaf first, then any intermediates; the root is never in
// it, because the root is the thing compiled into this binary and a chain
// that carried its own root would be proving nothing.
type Envelope struct {
	Chain []string `json:"chain"`
	Alg   string   `json:"alg"`
	Sig   string   `json:"sig"` // base64, ASN.1 ECDSA signature
}

// Verify checks env against the exact bytes of manifestJSON and returns
// the manifest only if everything holds.
//
// The signature is checked over the bytes as fetched, before any parsing,
// so what was signed and what is read are the same thing. A design that
// unmarshals first and re-marshals to verify is a design where a JSON
// quirk is a signature bypass.
//
// now is passed rather than read so the caller can be tested, and is the
// agent's own clock: a machine whose clock is years wrong will refuse to
// update, which is the safe direction.
func Verify(manifestJSON []byte, env *Envelope, roots *x509.CertPool, now time.Time) (*Manifest, error) {
	if env == nil || env.Alg != AlgECDSAP256SHA256 {
		return nil, ErrSignature
	}
	if len(env.Chain) == 0 {
		return nil, ErrSignature
	}
	certs := make([]*x509.Certificate, 0, len(env.Chain))
	for _, p := range env.Chain {
		blk, _ := pem.Decode([]byte(p))
		if blk == nil || blk.Type != "CERTIFICATE" {
			return nil, ErrSignature
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, ErrSignature
		}
		certs = append(certs, c)
	}
	leaf := certs[0]
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	// KeyUsages pins code signing: a root that also issues certificates
	// for anything else (and the FOG PKI issues plenty) must not have
	// one of those able to sign a release. Without this line a server or
	// client certificate under the same root would verify here.
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inter,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}); err != nil {
		return nil, ErrSignature
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, ErrSignature
	}
	sig, err := base64.StdEncoding.DecodeString(env.Sig)
	if err != nil {
		return nil, ErrSignature
	}
	sum := sha256.Sum256(manifestJSON)
	if !ecdsa.VerifyASN1(pub, sum[:], sig) {
		return nil, ErrSignature
	}
	var m Manifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		// A signature that verifies over bytes that are not a manifest
		// is not a signature problem, but it is not something to carry
		// on from either.
		return nil, fmt.Errorf("release: signed manifest is not readable: %w", err)
	}
	if !m.Expires.IsZero() && now.After(m.Expires) {
		return nil, ErrStale
	}
	return &m, nil
}

// Fresh reports whether m may be accepted by an agent whose highest
// previously accepted sequence is seen. Separate from Verify because it
// needs state Verify has no business reading, and because the answer is
// about this agent rather than about the manifest.
func Fresh(m *Manifest, seen int64) error {
	if m.Sequence < seen {
		return ErrStale
	}
	return nil
}

// Find returns the artifact for one version on one platform.
func (m *Manifest) Find(version, goos, goarch string) (*Artifact, error) {
	v, ok := m.Versions[version]
	if !ok {
		return nil, ErrNoArtifact
	}
	for i := range v.Artifacts {
		if v.Artifacts[i].OS == goos && v.Artifacts[i].Arch == goarch {
			a := v.Artifacts[i]
			if a.SHA256 == "" || a.URL == "" {
				return nil, ErrNoArtifact
			}
			return &a, nil
		}
	}
	return nil, ErrNoArtifact
}

// CopyVerified streams src into dst and returns ErrHash unless the bytes
// are exactly what the artifact declared.
//
// Hashed while streaming rather than after, so a hostile or broken mirror
// cannot make the agent write an arbitrary amount to disk: the size is a
// hard stop, not a check at the end.
func CopyVerified(dst io.Writer, src io.Reader, a *Artifact) error {
	h := sha256.New()
	// One byte past the declared size is already a mismatch, so read at
	// most that and let a longer body fail on the count.
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, a.Size+1))
	if err != nil {
		return err
	}
	if n != a.Size {
		return ErrHash
	}
	if !equalHex(hex.EncodeToString(h.Sum(nil)), a.SHA256) {
		return ErrHash
	}
	return nil
}

// equalHex compares two hex digests without caring about case. Not
// constant time on purpose: both sides are public values, and a digest
// the attacker already knows is not a secret to leak.
func equalHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
