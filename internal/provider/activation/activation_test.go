package activation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/FOGProject/fog-agent/internal/provider"
	"github.com/FOGProject/fog-agent/internal/secret"
)

// A Microsoft-published KMS client setup key (Windows 11 Pro): public, and
// real in shape, which is what the tests need.
const key = "W269N-WFGWX-YVC9B-4J6C9-T83GX"

type calls struct {
	installed string
	activated bool
}

func fake(t *testing.T, have License, installErr, activateErr error) *calls {
	t.Helper()
	c := &calls{}
	oldQ, oldI, oldA := query, install, activate
	query = func(context.Context) (License, error) { return have, nil }
	install = func(_ context.Context, k string) error { c.installed = k; return installErr }
	activate = func(context.Context) error { c.activated = true; return activateErr }
	t.Cleanup(func() { query, install, activate = oldQ, oldI, oldA })
	return c
}

func ensure(k string) provider.Result {
	return Ensure(context.Background(), Desired{Key: secret.New(k)})
}

// TestEnsureLeavesALicensedMatchingKeyAlone pins idempotence: the key is
// already installed and Windows is licensed, so nothing is called.
func TestEnsureLeavesALicensedMatchingKeyAlone(t *testing.T) {
	c := fake(t, License{Partial: "T83GX", Licensed: true}, nil, nil)
	if r := ensure(key); r.Status != provider.StatusUnchanged || c.installed != "" || c.activated {
		t.Fatalf("want unchanged and no calls, got %+v %+v", r, c)
	}
}

// TestEnsureActivatesAMatchingUnlicensedKey pins the second row of §4: the
// key is right, so it is not installed again, only activated.
func TestEnsureActivatesAMatchingUnlicensedKey(t *testing.T) {
	c := fake(t, License{Partial: "t83gx"}, nil, nil)
	if r := ensure(key); r.Status != provider.StatusApplied || c.installed != "" || !c.activated {
		t.Fatalf("want activate only, got %+v %+v", r, c)
	}
}

// TestEnsureInstallsWhenNoKeyIsInstalled pins the legacy client's
// EndsWith("") defect as fixed: an empty partial key matches nothing.
func TestEnsureInstallsWhenNoKeyIsInstalled(t *testing.T) {
	c := fake(t, License{Partial: ""}, nil, nil)
	r := ensure(strings.ToLower(key))
	if r.Status != provider.StatusApplied || c.installed != key || !c.activated {
		t.Fatalf("want install (upper-cased) then activate, got %+v %+v", r, c)
	}
}

// TestEnsureReportsARefusedInstallAsFailed pins that a refused key holds
// the revision, and that the detail names the key only by its last group.
func TestEnsureReportsARefusedInstallAsFailed(t *testing.T) {
	c := fake(t, License{Partial: "AAAAA", Licensed: true}, errors.New("0xC004F069"), nil)
	r := ensure(key)
	if r.Status != provider.StatusFailed || c.activated || !strings.Contains(r.Detail, "0xC004F069") {
		t.Fatalf("want failed, no activate, code in detail; got %+v %+v", r, c)
	}
	if strings.Contains(r.Detail, key[:23]) {
		t.Fatalf("detail carries the key: %q", r.Detail)
	}
}

// TestEnsureReportsAnUnfinishedActivationAsApplied pins §4: the key is in
// place, Windows retries activation, and failed would reinstall every poll.
func TestEnsureReportsAnUnfinishedActivationAsApplied(t *testing.T) {
	fake(t, License{}, nil, errors.New("0xC004F074"))
	if r := ensure(key); r.Status != provider.StatusApplied || !strings.Contains(r.Detail, "activation pending: 0xC004F074") {
		t.Fatalf("want applied with the code, got %+v", r)
	}
}

// TestEnsureRefusesAMalformedKey pins that nothing reaches Windows unless
// the key has the one shape the server sends.
func TestEnsureRefusesAMalformedKey(t *testing.T) {
	c := fake(t, License{}, nil, nil)
	for _, k := range []string{"", "W269NWFGWXYVC9B4J6C9T83GX", key + "-X"} {
		if r := ensure(k); r.Status != provider.StatusFailed || c.installed != "" {
			t.Fatalf("%q: want failed and no install, got %+v", k, r)
		}
	}
}

// TestDesiredCarriesTheKeyInAndNeverOut pins the secret handling: the key
// arrives from the wire and never leaves through a marshaler.
func TestDesiredCarriesTheKeyInAndNeverOut(t *testing.T) {
	var d Desired
	if err := json.Unmarshal([]byte(`{"key":"`+key+`"}`), &d); err != nil || d.Key.Reveal() != key {
		t.Fatalf("unmarshal: %v %q", err, d.Key.Reveal())
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), key[:5]) {
		t.Fatalf("marshaled key: %s", b)
	}
}
