package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/identity"
	"github.com/FOGProject/fog-agent/internal/provider/directoryjoin"
	"github.com/FOGProject/fog-agent/internal/provider/update"
	"github.com/FOGProject/fog-agent/internal/reboot"
)

func writeStartupProbation(t *testing.T, st *enroll.State, deadline time.Time) {
	t.Helper()
	p := update.Probation{From: "1.0.0", To: "1.1.0", Sequence: 9, Deadline: deadline}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir, "update-probation.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBeforeDirectoryNetworkChecksDueProbationBeforePrepare(t *testing.T) {
	for _, tc := range []struct {
		name       string
		revertErr  error
		wantRevert int
		wantPrep   int
		wantResult error
	}{
		{name: "rollback succeeds", wantRevert: 1, wantPrep: 0, wantResult: errUpdated},
		{name: "rollback fails and preparation continues", revertErr: errors.New("restore failed"), wantRevert: 1, wantPrep: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := enroll.Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			writeStartupProbation(t, st, time.Now().Add(-time.Minute))
			reverts, prepares := 0, 0
			err = beforeDirectoryNetwork(func() bool {
				return checkProbationWithRevert(st, &sayer{}, func(reason string) error {
					reverts++
					if !strings.Contains(reason, "no successful poll before") {
						t.Errorf("revert reason = %q", reason)
					}
					return tc.revertErr
				})
			}, func() error {
				prepares++
				return nil
			})
			if !errors.Is(err, tc.wantResult) {
				t.Fatalf("beforeDirectoryNetwork() error = %v, want %v", err, tc.wantResult)
			}
			if reverts != tc.wantRevert || prepares != tc.wantPrep {
				t.Fatalf("calls: revert=%d prepare=%d, want %d/%d", reverts, prepares, tc.wantRevert, tc.wantPrep)
			}
		})
	}
}

func TestBeforeDirectoryNetworkLeavesFutureProbationAndPrepares(t *testing.T) {
	st, err := enroll.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeStartupProbation(t, st, time.Now().Add(time.Hour))
	reverts, prepares := 0, 0
	err = beforeDirectoryNetwork(func() bool {
		return checkProbationWithRevert(st, &sayer{}, func(string) error { reverts++; return nil })
	}, func() error { prepares++; return nil })
	if err != nil || reverts != 0 || prepares != 1 {
		t.Fatalf("future probation: err=%v revert=%d prepare=%d", err, reverts, prepares)
	}
}

func TestPrepareWindowsRunReportsRetryOnlyForUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		header string
		wantOn bool
		// wantRetry separates "could not ask" (ask again next poll) from
		// "asked and not offered" (off until restart).
		wantRetry bool
	}{
		// Neither unavailable nor unsupported stops the agent any more: the
		// feature is off and the agent works as it did before it.
		{name: "transport unavailable", wantRetry: true},
		{name: "reachable unsupported", status: http.StatusOK},
		{name: "supported", status: http.StatusOK, header: enroll.DirectoryJoinTargetFeature + ", " + enroll.DirectoryTaskOperationsFeature, wantOn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := enroll.Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/agent/v1/enroll" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected", http.StatusInternalServerError)
					return
				}
				if tc.header != "" {
					w.Header().Set("X-FOG-Agent-Features", tc.header)
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			client := &enroll.Client{ServerURL: server.URL, HTTP: server.Client()}
			if tc.name == "transport unavailable" {
				client.HTTP = &http.Client{Transport: startupRoundTripper(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("offline")
				})}
			}
			on, retry, err := prepareWindowsRun(context.Background(), st, client, &sayer{})
			if err != nil {
				t.Fatalf("prepareWindowsRun() error = %v", err)
			}
			if on != tc.wantOn || retry != tc.wantRetry {
				t.Fatalf("prepareWindowsRun() on=%t retry=%t, want on=%t retry=%t", on, retry, tc.wantOn, tc.wantRetry)
			}
			// An off outcome leaves the key to the caller; only on settles it.
			if (st.Key != nil) != tc.wantOn {
				t.Fatalf("key present = %t, want %t", st.Key != nil, tc.wantOn)
			}
			if requests != 0 && requests != 1 {
				t.Fatalf("probe count = %d, want at most one", requests)
			}
			if tc.name == "supported" && requests != 1 {
				t.Fatalf("probe count = %d, want one", requests)
			}
			if _, err := os.Stat(filepath.Join(st.Dir, "directory-rejoin-attempt.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected operation marker: %v", err)
			}
		})
	}
}

type startupRoundTripper func(*http.Request) (*http.Response, error)

func (f startupRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPrepareWindowsRunIdentityMismatchStopsBeforePolling(t *testing.T) {
	st, err := enroll.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureKey(identity.Host{}); err != nil {
		t.Fatal(err)
	}
	st.Config.HostID = 101
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "agent-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, leafTemplate, leafTemplate, &st.Key.PublicKey, st.Key)
	if err != nil {
		t.Fatal(err)
	}
	st.Cert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	var probeCalls, identityCalls, fallbackCalls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/v1/enroll":
			probeCalls++
			w.Header().Set("X-FOG-Agent-Features", enroll.DirectoryJoinTargetFeature+", "+enroll.DirectoryTaskOperationsFeature)
		case "/agent/v1/identity/check":
			identityCalls++
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":   "identity_blocked",
				"identity": map[string]any{"status": "mismatch", "host_id": 202, "principal_host_id": 101},
			})
		default:
			fallbackCalls++
			http.Error(w, "fallback forbidden", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	client, err := enroll.NewClient(server.URL, ca)
	if err != nil {
		t.Fatal(err)
	}
	if on, retry, err := prepareWindowsRun(context.Background(), st, client, &sayer{}); err != nil || !on || retry {
		t.Fatalf("prepareWindowsRun() on=%t retry=%t error = %v", on, retry, err)
	}
	if probeCalls != 1 || identityCalls != 1 || fallbackCalls != 0 {
		t.Fatalf("requests: probe=%d identity=%d poll/native/fallback=%d", probeCalls, identityCalls, fallbackCalls)
	}
	if st.Config.HostID != 0 || st.Key == nil || len(st.Cert) != 0 {
		t.Fatalf("identity mismatch did not rotate enrollment credentials: host=%d key=%t cert=%d", st.Config.HostID, st.Key != nil, len(st.Cert))
	}
}

func TestPrepareDirectoryWithoutCertificateBindsKeyToLiveIdentityAndPreservesUncertainty(t *testing.T) {
	for _, tc := range []struct {
		name          string
		keyIdentity   string
		liveIdentity  string
		wantRotate    bool
		wantHostID    int
		withUncertain bool
	}{
		{name: "copied key follows different live machine", keyIdentity: "machine-A", liveIdentity: "machine-B", wantRotate: true, withUncertain: true},
		{name: "same machine retains key and enrollment binding", keyIdentity: "machine-A", liveIdentity: "machine-A", wantHostID: 101},
		{name: "new key initialized for live machine", liveIdentity: "machine-B"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := enroll.Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			keyIdentity := identity.Host{}
			if tc.keyIdentity != "" {
				keyIdentity = machine(tc.keyIdentity)
			}
			if tc.keyIdentity != "" {
				if _, err := st.EnsureKey(keyIdentity); err != nil {
					t.Fatal(err)
				}
			}
			st.Config.SystemTrust = true
			st.Config.HostID = 101
			if tc.withUncertain {
				fp, err := st.KeyFingerprint()
				if err != nil {
					t.Fatal(err)
				}
				plan := directoryjoin.RejoinPlan{
					TaskID: 71, Version: 2, Operation: "rebind",
					AttemptID: strings.Repeat("c", 32), HostID: 101,
					ConfigRevision: strings.Repeat("d", 32), Phase: "ready",
					Name: "B", Domain: "corp.example", Mode: "existing", DC: "dc.corp.example",
					ExpectedCurrent: "A", ExpectedPending: "A",
					Target: &directoryjoin.RejoinObject{GUID: "22222222-2222-2222-2222-222222222222", SID: "S-1-5-21-101-2", Account: "B$"},
				}
				if err := directoryjoin.ReserveAttempt(st.Dir, &directoryjoin.Attempt{
					Version: 2, Fingerprint: fp, Plan: plan, Phase: "unknown",
				}); err != nil {
					t.Fatal(err)
				}
			}
			var beforeFingerprint string
			if st.Key != nil {
				beforeFingerprint, err = st.KeyFingerprint()
				if err != nil {
					t.Fatal(err)
				}
			}
			var markerBefore []byte
			if tc.withUncertain {
				markerBefore, err = os.ReadFile(directoryjoin.AttemptPath(st.Dir))
				if err != nil {
					t.Fatal(err)
				}
			}

			featureProbes := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				featureProbes++
				if r.Method != http.MethodGet || r.URL.Path != "/agent/v1/enroll" {
					t.Errorf("unexpected no-certificate request %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected request", http.StatusInternalServerError)
					return
				}
				w.Header().Set("X-FOG-Agent-Features", enroll.DirectoryJoinTargetFeature+", "+enroll.DirectoryTaskOperationsFeature)
			}))
			defer server.Close()
			st.Config.ServerURL = server.URL
			live := identity.Host{}
			if tc.liveIdentity != "" {
				live = machine(tc.liveIdentity)
			}
			stubIdentity(t, live)
			client := &enroll.Client{ServerURL: server.URL, HTTP: server.Client()}
			if on, retry, err := prepareDirectoryIdentity(context.Background(), st, client, &sayer{}); err != nil || !on || retry {
				t.Fatalf("prepareDirectoryIdentity: on=%t retry=%t err=%v", on, retry, err)
			}

			if featureProbes != 1 {
				t.Fatalf("feature probes=%d, want 1", featureProbes)
			}
			if st.Key == nil || st.Cert != nil || !st.Config.SystemTrust {
				t.Fatalf("key/certificate/trust state incorrect: key=%t cert=%t trust=%t", st.Key != nil, st.Cert != nil, st.Config.SystemTrust)
			}
			afterFingerprint, err := st.KeyFingerprint()
			if err != nil {
				t.Fatal(err)
			}
			if beforeFingerprint != "" && (afterFingerprint != beforeFingerprint) != tc.wantRotate {
				t.Fatalf("key rotation mismatch: rotated=%t want=%t before=%q after=%q", afterFingerprint != beforeFingerprint, tc.wantRotate, beforeFingerprint, afterFingerprint)
			}
			if st.Config.HostID != tc.wantHostID {
				t.Fatalf("HostID=%d, want %d", st.Config.HostID, tc.wantHostID)
			}
			if st.Identity == nil || st.Identity.SystemUUID != live.SystemUUID || st.Identity.SystemSerial != live.SystemSerial ||
				st.Identity.BoardSerial != live.BoardSerial || st.Identity.ChassisAsset != live.ChassisAsset {
				t.Fatalf("local identity was not bound to live machine: got=%+v want=%+v", st.Identity, live)
			}
			if tc.withUncertain {
				markerPath := directoryjoin.AttemptPath(st.Dir)
				afterMarker, err := os.ReadFile(markerPath)
				if err != nil || !bytes.Equal(markerBefore, afterMarker) {
					t.Fatalf("uncertain marker changed during key rotation: equal=%t err=%v", bytes.Equal(markerBefore, afterMarker), err)
				}
				if err := processRejoin(context.Background(), st, client, nil, &sayer{}); err == nil {
					t.Fatal("uncertain marker did not stop normal processing")
				}
				finalMarker, err := os.ReadFile(markerPath)
				if err != nil || !bytes.Equal(markerBefore, finalMarker) || featureProbes != 1 {
					t.Fatalf("uncertain marker was replayed or changed: equal=%t probes=%d err=%v", bytes.Equal(markerBefore, finalMarker), featureProbes, err)
				}
			}
		})
	}
}

// A server that does not offer both directory features must leave the agent
// working as before the feature: no error, directory mode off, key settled.
func TestPrepareDirectoryIdentityOffWhenServerLacksFeatures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		header string
	}{
		{name: "404", status: http.StatusNotFound},
		{name: "200 with only the target feature", status: http.StatusOK, header: enroll.DirectoryJoinTargetFeature},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := enroll.Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			stubIdentity(t, machine("lab"))
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/agent/v1/enroll" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if tc.header != "" {
					w.Header().Set("X-FOG-Agent-Features", tc.header)
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			client := &enroll.Client{ServerURL: server.URL, HTTP: server.Client()}
			out := &sayer{}
			on, retry, err := prepareDirectoryIdentity(context.Background(), st, client, out)
			if err != nil || on || retry {
				t.Fatalf("prepareDirectoryIdentity() = %t, %t, %v; want false, false, nil", on, retry, err)
			}
			if st.Key != nil {
				t.Fatal("prepareDirectoryIdentity settled a key itself; that is the caller's job")
			}
			// The caller (runAgent) then runs the pre-feature guard.
			if err := localCloneGuard(st, out); err != nil || st.Key == nil {
				t.Fatalf("localCloneGuard: key=%t err=%v", st.Key != nil, err)
			}
		})
	}
}

// A probe that could not reach the server is retried, and a later probe
// against a server that now offers both features turns the feature on.
func TestPrepareDirectoryIdentityRetriesAfterUnavailableProbe(t *testing.T) {
	st, err := enroll.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stubIdentity(t, machine("lab"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-FOG-Agent-Features", enroll.DirectoryJoinTargetFeature+", "+enroll.DirectoryTaskOperationsFeature)
	}))
	defer server.Close()
	offline := &enroll.Client{ServerURL: server.URL, HTTP: &http.Client{Transport: startupRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network not up yet")
	})}}
	on, retry, err := prepareDirectoryIdentity(context.Background(), st, offline, &sayer{})
	if err != nil || on || !retry {
		t.Fatalf("offline probe = on=%t retry=%t err=%v; want off with retry", on, retry, err)
	}
	online := &enroll.Client{ServerURL: server.URL, HTTP: server.Client()}
	on, retry, err = prepareDirectoryIdentity(context.Background(), st, online, &sayer{})
	if err != nil || !on || retry {
		t.Fatalf("second probe = on=%t retry=%t err=%v; want on", on, retry, err)
	}
}

// The directory-rejoin reboot filter must not depend on directory mode. A
// persisted reason whose marker already requested its reboot must not reboot
// again while directory mode is off.
func TestCoordinateFiltersRejoinReasonWhenDirectoryModeOff(t *testing.T) {
	oldOn, oldRetry, oldWin := directoryOn, directoryRetry, coordinateWindows
	directoryOn, directoryRetry, coordinateWindows = false, false, true
	t.Cleanup(func() { directoryOn, directoryRetry, coordinateWindows = oldOn, oldRetry, oldWin })
	st, plan := newDirectoryBoundaryState(t)
	fingerprint, err := st.KeyFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	attempt := &directoryjoin.Attempt{Version: 2, Fingerprint: fingerprint, Plan: plan, Phase: "awaiting_reboot", RebootRequested: true}
	if err = directoryjoin.ReserveAttempt(st.Dir, attempt); err != nil {
		t.Fatal(err)
	}
	st.Config.PendingReboot = []reboot.Reason{{Source: reboot.SourceDirectoryRejoin, Detail: "already requested", Mode: reboot.ModeReboot}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()
	oldUsers, oldRequest := rebootUsers, requestReboot
	t.Cleanup(func() { rebootUsers, requestReboot = oldUsers, oldRequest })
	rebootUsers = func() (int, error) { return 0, nil }
	rebootCalls := 0
	requestReboot = func(string, time.Duration, string) error { rebootCalls++; return nil }
	client := &enroll.Client{ServerURL: server.URL, HTTP: server.Client()}
	if err = coordinate(context.Background(), st, client, &sayer{}); err != nil {
		t.Fatalf("coordinate: %v", err)
	}
	if rebootCalls != 0 {
		t.Fatalf("a rejoin reason whose reboot was already requested rebooted again: calls=%d", rebootCalls)
	}
}
