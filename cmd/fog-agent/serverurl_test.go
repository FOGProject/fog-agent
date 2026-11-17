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

// setHTTPServer rewrites config.json's server_url to the http:// form of the
// enrolled server, as an install made before the move left it.
func setHTTPServer(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	raw["server_url"] = "http://fog.example.org/fog"
	if b, err = json.Marshal(raw); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var log bytes.Buffer
	saved := logOut
	logOut = &log
	// movedSaid lives for the process; each test is a fresh start.
	movedSaid = ""
	t.Cleanup(func() { logOut = saved; movedSaid = "" })
	return &log
}

// Forum topic 18320: an agent installed with an http:// address enrolled and
// then failed every poll. An install already in that state moves to
// https:// on its next start, keeps its enrollment, says so, and saves it.
func TestAnHTTPAddressInConfigMovesToHTTPSAndKeepsTheEnrollment(t *testing.T) {
	dir := enrolledState(t, false)
	setHTTPServer(t, dir)
	stubCheckTrust(t, nil)
	stubProbe(t, trustedProbe)
	log := captureLog(t)

	st, err := settleTrust(flagsFor(t, "--dir", dir), &sayer{})
	if err != nil {
		t.Fatalf("setup with an http:// address in config.json: %v", err)
	}
	if st.Config.ServerURL != "https://fog.example.org/fog" {
		t.Errorf("server = %q; want https://fog.example.org/fog", st.Config.ServerURL)
	}
	if st.Config.HostID != 42 {
		t.Errorf("host id = %d; moving the address must keep the enrollment", st.Config.HostID)
	}
	if !strings.Contains(log.String(), "http://fog.example.org/fog is not https://") {
		t.Errorf("the move was not logged; log:\n%s", log.String())
	}
	reloaded, err := enroll.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ServerURLMoved != "" || reloaded.Config.ServerURL != "https://fog.example.org/fog" {
		t.Errorf("config.json still holds %q after setup; the move was not saved", reloaded.ServerURLMoved)
	}
}

// The service start (run) on an install that saved http://, which is what
// the forum reporter's cloned machines do: moved, enrollment kept, saved.
func TestServiceStartMovesAnHTTPAddressInConfig(t *testing.T) {
	dir := enrolledState(t, false)
	setHTTPServer(t, dir)
	log := captureLog(t)

	st, err := openState(flagsFor(t, "--dir", dir))
	if err != nil {
		t.Fatalf("openState with an http:// address in config.json: %v", err)
	}
	if st.Config.ServerURL != "https://fog.example.org/fog" || st.Config.HostID != 42 {
		t.Errorf("server = %q, host id = %d; want https://fog.example.org/fog and 42", st.Config.ServerURL, st.Config.HostID)
	}
	if n := strings.Count(log.String(), "is not https://"); n != 1 {
		t.Errorf("the move was logged %d times; want once. log:\n%s", n, log.String())
	}
	if again, err := openState(flagsFor(t, "--dir", dir)); err != nil || again.ServerURLMoved != "" {
		t.Errorf("the second start moved the address again (err %v); it was not saved", err)
	}
}

// config.json and --server both http://, as a cloned image set up again
// gives: one move, one line.
func TestSetupLogsTheMoveOnceWhenConfigAndFlagAreBothHTTP(t *testing.T) {
	dir := enrolledState(t, false)
	setHTTPServer(t, dir)
	stubCheckTrust(t, nil)
	stubProbe(t, trustedProbe)
	log := captureLog(t)

	if _, err := settleTrust(flagsFor(t, "--dir", dir, "--server", "http://fog.example.org/fog"), &sayer{}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if n := strings.Count(log.String(), "is not https://"); n != 1 {
		t.Errorf("the move was logged %d times; want once. log:\n%s", n, log.String())
	}
}

// An address with user:password@ in it must not put the password in the log.
func TestTheMovedLineRedactsAPassword(t *testing.T) {
	line := movedServerLine("http://u:s3cret@fog.lab/fog", "https://u:s3cret@fog.lab/fog")
	if strings.Contains(line, "s3cret") {
		t.Fatalf("the log line carries the password: %q", line)
	}
}

// The MSI and the installer wizard pass --server. An http:// value is moved
// before anything is enrolled against it, and the move is logged.
func TestAnHTTPServerFlagMovesToHTTPS(t *testing.T) {
	dir := enrolledState(t, false)
	stubCheckTrust(t, nil)
	stubProbe(t, trustedProbe)
	log := captureLog(t)

	st, err := settleTrust(flagsFor(t, "--dir", dir, "--server", "http://fog.example.org/fog"), &sayer{})
	if err != nil {
		t.Fatalf("setup with --server http://: %v", err)
	}
	if st.Config.ServerURL != "https://fog.example.org/fog" {
		t.Errorf("server = %q; want https://fog.example.org/fog", st.Config.ServerURL)
	}
	if !strings.Contains(log.String(), "is not https://; using https://fog.example.org/fog") {
		t.Errorf("the move was not logged; log:\n%s", log.String())
	}
}
