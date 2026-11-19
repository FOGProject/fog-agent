package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/provider/directoryjoin"
	"github.com/FOGProject/fog-agent/internal/provider/snapin"
	"github.com/FOGProject/fog-agent/internal/provider/software"
)

// Software runs before snapin whatever order the server sent (forum 18322:
// a Sysprep snapin ran before Chocolatey finished). Servers released before
// the rule list snapin first. Every other capability keeps the server's
// order, and a rename in the domain still runs directory first.
func TestCapabilityOrderRunsSoftwareBeforeSnapin(t *testing.T) {
	cases := []struct {
		name string
		caps []string
		dir  *directoryjoin.Policy
		want []string
	}{
		{"snapin before software is swapped",
			[]string{"hostname", "snapin", "printers", "software"}, nil,
			[]string{"hostname", "software", "snapin", "printers"}},
		{"already correct is unchanged",
			[]string{"hostname", "software", "snapin", "printers"}, nil,
			[]string{"hostname", "software", "snapin", "printers"}},
		{"software only is unchanged",
			[]string{"power", "software", "printers"}, nil,
			[]string{"power", "software", "printers"}},
		{"snapin only is unchanged",
			[]string{"hostname", "snapin", "printers"}, nil,
			[]string{"hostname", "snapin", "printers"}},
		{"rename still puts directory first",
			[]string{"hostname", "snapin", "directory", "software"},
			&directoryjoin.Policy{Domain: "corp.example.com", RenameTo: "WS-NEW"},
			[]string{"directory", "hostname", "software", "snapin"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := slices.Clone(tc.caps)
			got := capabilityOrder(&enroll.DesiredState{Capabilities: in, Directory: tc.dir})
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
			if !slices.Equal(in, tc.caps) {
				t.Errorf("the server's list was modified: %v", in)
			}
		})
	}
}

// Only a flagged entry that did not succeed holds the snapin queue.
func TestHoldsSnapins(t *testing.T) {
	boom := errors.New("report did not land")
	cases := []struct {
		name    string
		flag    bool
		outcome string
		err     error
		want    bool
	}{
		{"flag off, failed", false, enroll.OutcomeFailed, nil, false},
		{"flag off, retry", false, enroll.OutcomeRetry, nil, false},
		{"flag off, report error", false, "", boom, false},
		{"flag on, failed", true, enroll.OutcomeFailed, nil, true},
		{"flag on, retry", true, enroll.OutcomeRetry, nil, true},
		{"flag on, report error", true, "", boom, true},
		{"flag on, success", true, enroll.OutcomeSuccess, nil, false},
		{"flag on, reboot", true, enroll.OutcomeReboot, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := software.Entry{Package: "git", StopSnapins: tc.flag}
			if got := holdsSnapins(e, tc.outcome, tc.err); got != tc.want {
				t.Errorf("holdsSnapins = %t, want %t", got, tc.want)
			}
		})
	}
}

// softwareBeforeSnapin never modifies the server's list, and leaves a list
// that does not need the move as it is.
func TestSoftwareBeforeSnapin(t *testing.T) {
	in := []string{"snapin", "power", "software"}
	got := softwareBeforeSnapin(in)
	if want := []string{"software", "snapin", "power"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if want := []string{"snapin", "power", "software"}; !slices.Equal(in, want) {
		t.Errorf("the input list was modified: %v", in)
	}
	if got := softwareBeforeSnapin(nil); len(got) != 0 {
		t.Errorf("nil list: got %v", got)
	}
}

// A server that omits stop_snapins (any server older than this field) reads
// false; one that sends true reads true.
func TestEntryDecodesStopSnapins(t *testing.T) {
	for _, tc := range []struct {
		name, js string
		want     bool
	}{
		{"omitted", `{"id":3,"backend":"choco","package":"git"}`, false},
		{"false", `{"id":3,"package":"git","stop_snapins":false}`, false},
		{"true", `{"id":3,"package":"git","stop_snapins":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var e software.Entry
			if err := json.Unmarshal([]byte(tc.js), &e); err != nil {
				t.Fatal(err)
			}
			if e.StopSnapins != tc.want {
				t.Errorf("StopSnapins = %t, want %t", e.StopSnapins, tc.want)
			}
		})
	}
}

// reconcile holds the snapin queue when a stop_snapins software entry did not
// install, and leaves the revision unapplied; when the entry succeeds the
// queue starts. The software entry names a backend this agent lacks, so it
// reports cannot_run on every platform; the server's answer decides the
// outcome. The snapin's payload fetch is the sign the queue started: the
// server 404s it, so the run itself stops at the fetch.
func TestReconcileHoldsSnapinsForAFailedStopSnapinsEntry(t *testing.T) {
	for _, tc := range []struct {
		name        string
		outcome     string
		wantHeld    bool
		wantPayload int32
	}{
		{"failed holds", enroll.OutcomeFailed, true, 0},
		{"retry holds", enroll.OutcomeRetry, true, 0},
		{"success runs the snapin", enroll.OutcomeSuccess, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payloads, snapinResults int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/agent/v1/result":
					var req enroll.ResultRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					if req.Capability == "snapin" {
						atomic.AddInt32(&snapinResults, 1)
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "outcome": tc.outcome})
				case strings.HasPrefix(r.URL.Path, "/agent/v1/payload/snapin/"):
					atomic.AddInt32(&payloads, 1)
					http.NotFound(w, r)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			client := &enroll.Client{ServerURL: srv.URL, HTTP: srv.Client()}
			st, err := enroll.Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			st.Config.AppliedRevision = "old"

			var log bytes.Buffer
			oldOut := logOut
			logOut = &log
			t.Cleanup(func() { logOut = oldOut })

			// snapin is listed first, as a server older than the rule sends it.
			desired := &enroll.DesiredState{
				Revision:     "new",
				Capabilities: []string{"snapin", "software"},
				Software: &software.Policy{Entries: []software.Entry{
					{ID: 3, Backend: "nosuchbackend", Package: "git", State: "present", StopSnapins: true},
				}},
				Snapins: []snapin.Task{{ID: 7, SnapinID: 1, Name: "sysprep", File: "sysprep.exe"}},
			}
			err = reconcile(t.Context(), st, client, desired, &sayer{})

			if err == nil {
				t.Error("reconcile returned nil; the revision must stay unapplied")
			}
			if st.Config.AppliedRevision != "old" {
				t.Errorf("AppliedRevision = %q, want it unchanged", st.Config.AppliedRevision)
			}
			if got := atomic.LoadInt32(&payloads); got != tc.wantPayload {
				t.Errorf("snapin payload requests = %d, want %d", got, tc.wantPayload)
			}
			waiting := strings.Contains(log.String(), "snapin: waiting")
			if waiting != tc.wantHeld {
				t.Errorf("log has \"snapin: waiting\" = %t, want %t\n%s", waiting, tc.wantHeld, log.String())
			}
			if tc.wantHeld && atomic.LoadInt32(&snapinResults) != 0 {
				t.Error("a snapin result was reported while the queue was held")
			}
		})
	}
}
