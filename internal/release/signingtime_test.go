package release

import (
	"crypto/elliptic"
	"crypto/x509"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// bodyAt builds a manifest that says when it was signed and when it expires.
func bodyAt(t *testing.T, signed, expires time.Time) []byte {
	t.Helper()
	m := Manifest{
		Channel: "stable", Sequence: 47, Signed: signed, Expires: expires,
		Versions: map[string]Version{"0.4.2": {Artifacts: []Artifact{
			{OS: "linux", Arch: "amd64", SHA256: strings.Repeat("ab", 32), Size: 12, URL: "https://example.invalid/a"},
		}}},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// leafValid mints a root and a code-signing leaf good for the given window.
func leafValid(t *testing.T, from, to time.Time) (*ca, *x509.CertPool) {
	t.Helper()
	root := newRoot(t, from.Add(-time.Hour), from.Add(20*365*24*time.Hour))
	leaf := newLeaf(t, root, []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}, from, to, elliptic.P256())
	pool := x509.NewCertPool()
	pool.AddCert(root.cert)
	return leaf, pool
}

// The point of the whole change. A manifest signed while the leaf was good
// must keep verifying after that leaf expires -- otherwise the day the leaf
// dies, every manifest it ever signed dies with it, and every agent mid
// rollout reports signature_invalid, which names the wrong cause.
func TestAManifestStillVerifiesAfterTheLeafThatSignedItExpired(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	leafDied := now.Add(-30 * 24 * time.Hour)
	signedOn := now.Add(-60 * 24 * time.Hour) // while the leaf was still good

	leaf, roots := leafValid(t, now.Add(-365*24*time.Hour), leafDied)
	body := bodyAt(t, signedOn, now.Add(30*24*time.Hour))

	if _, err := Verify(body, sign(t, leaf, body), roots, now); err != nil {
		t.Fatalf("a manifest signed while the leaf was valid must survive the leaf: %v", err)
	}
}

// The other half: honouring the signing time must not let a dead key keep
// producing new manifests.
func TestAnExpiredLeafCannotSignSomethingNew(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	leaf, roots := leafValid(t, now.Add(-365*24*time.Hour), now.Add(-24*time.Hour))
	body := bodyAt(t, now, now.Add(30*24*time.Hour)) // claims to be signed today

	if _, err := Verify(body, sign(t, leaf, body), roots, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("an expired leaf must not sign a new manifest; got %v", err)
	}
}

// And the attack that honouring the signing time invites: whoever holds a
// long-dead key backdates the claim into the window where it was valid.
// maxSignatureAge is what closes it -- every time that key was valid is by
// now too old to honour.
func TestBackdatingCannotResurrectALongExpiredLeaf(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	leafDied := now.Add(-300 * 24 * time.Hour)
	leaf, roots := leafValid(t, now.Add(-600*24*time.Hour), leafDied)

	// Backdated to a moment the leaf was genuinely valid, and claiming a
	// long future expiry so freshness alone would not catch it.
	body := bodyAt(t, now.Add(-320*24*time.Hour), now.Add(365*24*time.Hour))

	if _, err := Verify(body, sign(t, leaf, body), roots, now); !errors.Is(err, ErrStale) {
		t.Fatalf("a claim older than maxSignatureAge must be refused; got %v", err)
	}
}

// A manifest may not reach FORWARD into a certificate's validity by
// claiming to have been signed in the future. Clock skew lands here too
// and must be read conservatively.
//
// The leaf here is not yet valid -- NotBefore is tomorrow -- which is the
// only shape where the clamp changes the answer. An already-expired leaf
// would fail at any future time as well, so testing with one would pass
// whether the clamp existed or not.
func TestAFutureSignedTimeIsClampedToNow(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	notYetValid, roots := leafValid(t, now.Add(24*time.Hour), now.Add(365*24*time.Hour))

	// Claims to be signed two days out, when the leaf will be good.
	body := bodyAt(t, now.Add(48*time.Hour), now.Add(30*24*time.Hour))
	if _, err := Verify(body, sign(t, notYetValid, body), roots, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("a future signing claim must be clamped to now, not honoured; got %v", err)
	}

	// And the same certificate once it really is valid: the manifest is
	// fine, proving the refusal above was about the time and not about
	// something else wrong with the rig.
	later := now.Add(48 * time.Hour)
	body2 := bodyAt(t, later, later.Add(30*24*time.Hour))
	if _, err := Verify(body2, sign(t, notYetValid, body2), roots, later); err != nil {
		t.Fatalf("the same leaf must work once it is genuinely valid: %v", err)
	}
}

// Expiry is the publisher's own freshness statement and is always judged
// against the real clock -- never against Signed, or a manifest could
// declare itself eternally fresh by backdating.
func TestExpiryIsStillJudgedAgainstTheRealClock(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	leaf, roots := leafValid(t, now.Add(-365*24*time.Hour), now.Add(365*24*time.Hour))
	body := bodyAt(t, now.Add(-time.Hour), now.Add(-time.Minute)) // already expired

	if _, err := Verify(body, sign(t, leaf, body), roots, now); !errors.Is(err, ErrStale) {
		t.Fatalf("an expired manifest must still be refused; got %v", err)
	}
}

// Manifests published before the field existed carry no Signed, and must
// keep behaving exactly as they did: judged against now.
func TestAManifestWithNoSignedTimeIsStillJudgedAgainstNow(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

	good, roots := leafValid(t, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	body := bodyAt(t, time.Time{}, now.Add(30*24*time.Hour))
	if _, err := Verify(body, sign(t, good, body), roots, now); err != nil {
		t.Fatalf("a manifest with no signed time and a live leaf must verify: %v", err)
	}

	dead, roots2 := leafValid(t, now.Add(-365*24*time.Hour), now.Add(-24*time.Hour))
	body2 := bodyAt(t, time.Time{}, now.Add(30*24*time.Hour))
	if _, err := Verify(body2, sign(t, dead, body2), roots2, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("with no signed time an expired leaf must still fail; got %v", err)
	}
}
