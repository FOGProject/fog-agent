package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

// zeroConfig does to dir what a power loss did to the lab VM fos-deploy-test
// right after a self-update: config.json.prev holds the config, and
// config.json is the same length and all zero bytes.
func zeroConfig(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".prev", b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, len(b)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The MSI's setup step on a zeroed config.json. Before the fix it failed
// with "invalid character '\x00'", the install rolled back, and the machine
// was left with no service. Both trust branches must recover, keep the
// enrollment where the trust still verifies, log the recovery, and leave a
// config.json the next start can read.
func TestSetupRecoversAZeroedConfig(t *testing.T) {
	for _, tc := range []struct {
		name     string
		trustErr error
	}{
		{"trust still verifies", nil},
		{"stale trust is settled again", staleErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := enrolledState(t, false)
			zeroConfig(t, dir)
			stubCheckTrust(t, tc.trustErr)
			stubProbe(t, trustedProbe)
			var log bytes.Buffer
			saved := logOut
			logOut = &log
			defer func() { logOut = saved }()

			st, err := settleTrust(flagsFor(t, "--dir", dir), &sayer{})
			if err != nil {
				t.Fatalf("setup on a zeroed config.json: %v", err)
			}
			if !strings.Contains(log.String(), "started from config.json.prev") {
				t.Errorf("the recovery was not logged; log:\n%s", log.String())
			}
			if tc.trustErr == nil && st.Config.HostID != 42 {
				t.Errorf("host id = %d; want 42 from config.json.prev", st.Config.HostID)
			}
			var cfg enroll.Config
			b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
			if err := json.Unmarshal(b, &cfg); err != nil {
				t.Errorf("config.json after setup does not parse: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "config.json.damaged")); err != nil {
				t.Errorf("the damaged file was not kept: %v", err)
			}
		})
	}
}
