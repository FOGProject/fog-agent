package enroll

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestZeroedConfigFallsBackToPrev reproduces the lab VM fos-deploy-test: a
// power loss right after a self-update left config.json at its full length
// and every byte zero, while the config.json.prev the update had copied was
// intact. Load must start from the copy instead of failing every start.
func TestZeroedConfigFallsBackToPrev(t *testing.T) {
	dir := t.TempDir()
	good, err := json.Marshal(Config{ServerURL: "https://fog.example/fog", HostID: 211})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, configFile), make([]byte, len(good)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, configFile+".prev"), good, 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := Load(dir)
	if err != nil {
		t.Fatalf("Load with a zeroed config.json and a good .prev: %v", err)
	}
	if st.Config.HostID != 211 || st.Config.ServerURL != "https://fog.example/fog" {
		t.Errorf("config = %+v; want the values from config.json.prev", st.Config)
	}
	if st.ConfigRecovered == "" {
		t.Fatal("ConfigRecovered is empty; the caller has nothing to log")
	}

	// RepairConfig keeps the damaged file and writes the recovered one back,
	// so the next Load reads config.json cleanly and reports nothing.
	if err := st.RepairConfig(); err != nil {
		t.Fatalf("RepairConfig: %v", err)
	}
	damaged, err := os.ReadFile(filepath.Join(dir, configFile+".damaged"))
	if err != nil || len(damaged) != len(good) {
		t.Errorf("config.json.damaged = %d bytes, %v; want the %d-byte damaged file kept", len(damaged), err, len(good))
	}
	again, err := Load(dir)
	if err != nil {
		t.Fatalf("Load after RepairConfig: %v", err)
	}
	if again.ConfigRecovered != "" || again.Config.HostID != 211 {
		t.Errorf("after repair: recovered=%q host=%d; want a clean load of host 211", again.ConfigRecovered, again.Config.HostID)
	}
}

// TestZeroedConfigWithDamagedPrevFails covers a .prev that is itself
// unreadable: there is nothing safe to start from, so Load must refuse.
func TestZeroedConfigWithDamagedPrevFails(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{configFile, configFile + ".prev"} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 64), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("Load accepted a zeroed config.json with a zeroed .prev")
	}
}

// TestZeroedConfigWithoutPrevNamesTheFix pins the message when there is no
// copy to start from: it must say what to do, not only what the parser saw.
func TestZeroedConfigWithoutPrevNamesTheFix(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configFile), make([]byte, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load accepted a zeroed config.json with no .prev")
	}
	if !strings.Contains(err.Error(), "run setup again with --server") {
		t.Errorf("error %q does not say how to recover", err)
	}
}
