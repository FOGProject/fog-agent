package enroll

import (
	"reflect"
	"testing"

	"github.com/FOGProject/fog-agent/internal/identity"
)

// Everything in config.json but the way to the server describes the host
// the old key was bound to. A clone that kept the fact hashes would never
// send its own software list, because it matches what the original sent.
func TestEnsureKeyForgetsTheOldHostAndKeepsTheServer(t *testing.T) {
	dir := t.TempDir()
	st, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Config.ServerURL = "https://fog.example.org/fog"
	st.Config.PendingToken = "token"
	if _, err := st.EnsureKey(liveID("aaaa")); err != nil {
		t.Fatal(err)
	}
	st.Config.AppliedRevision = "rev"
	st.Config.SoftwareHash = "hash"
	if err := st.SaveIssued([]byte("CERT"), 105); err != nil {
		t.Fatal(err)
	}

	st2, _ := Load(dir)
	if regen, err := st2.EnsureKey(liveID("bbbb")); err != nil || !regen {
		t.Fatalf("clone: regen=%v err=%v", regen, err)
	}
	onDisk, _ := Load(dir)
	want := Config{ServerURL: "https://fog.example.org/fog", PendingToken: "token"}
	if !reflect.DeepEqual(onDisk.Config, want) {
		t.Fatalf("config after a new key:\n got %+v\nwant %+v", onDisk.Config, want)
	}
}

// The guard runs on every start now, so a firmware read that fails must not
// cost a working enrollment.
func TestEnsureKeyKeepsTheKeyWhenTheFirmwareCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	st, _ := Load(dir)
	if _, err := st.EnsureKey(liveID("aaaa")); err != nil {
		t.Fatal(err)
	}
	first := st.Key.PublicKey
	if err := st.SaveIssued([]byte("CERT"), 105); err != nil {
		t.Fatal(err)
	}

	st2, _ := Load(dir)
	var failed identity.Host
	failed.Warnings = []string{"GetSystemFirmwareTable: no RSMB table"}
	regen, err := st2.EnsureKey(failed)
	if err != nil || regen || !st2.Key.PublicKey.Equal(&first) || string(st2.Cert) != "CERT" {
		t.Fatalf("failed read: regen=%v err=%v cert=%q", regen, err, st2.Cert)
	}

	// An empty tuple with no warning is what the firmware said, so it is
	// compared, and it is not the machine the key was made for.
	if regen, _ := st2.EnsureKey(identity.Host{}); !regen {
		t.Fatal("an empty reading with no warning was taken as the same machine")
	}
}
