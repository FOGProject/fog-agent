package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/provider/snapin"
	"github.com/FOGProject/fog-agent/internal/provider/software"
)

// splitBeforeSoftware keeps the server's order inside each part and never
// modifies the queue it is given.
func TestSplitBeforeSoftware(t *testing.T) {
	task := func(id int, before bool) snapin.Task { return snapin.Task{ID: id, BeforeSoftware: before} }
	ids := func(ts []snapin.Task) []int {
		out := []int{}
		for _, task := range ts {
			out = append(out, task.ID)
		}
		return out
	}
	cases := []struct {
		name      string
		queue     []snapin.Task
		pre, post []int
	}{
		{"empty", nil, []int{}, []int{}},
		{"none marked", []snapin.Task{task(1, false), task(2, false)}, []int{}, []int{1, 2}},
		{"all marked", []snapin.Task{task(1, true), task(2, true)}, []int{1, 2}, []int{}},
		{"mixed keeps server order",
			[]snapin.Task{task(5, false), task(4, true), task(3, false), task(2, true), task(1, false)},
			[]int{4, 2}, []int{5, 3, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := ids(tc.queue)
			pre, post := splitBeforeSoftware(tc.queue)
			if got := ids(pre); !slices.Equal(got, tc.pre) {
				t.Errorf("pre = %v, want %v", got, tc.pre)
			}
			if got := ids(post); !slices.Equal(got, tc.post) {
				t.Errorf("post = %v, want %v", got, tc.post)
			}
			if !slices.Equal(ids(tc.queue), before) {
				t.Errorf("the queue was modified: %v", ids(tc.queue))
			}
		})
	}
}

// A server older than before_software omits the field, which reads false.
func TestTaskDecodesBeforeSoftware(t *testing.T) {
	for _, tc := range []struct {
		name, js string
		want     bool
	}{
		{"omitted", `{"task":7,"snapin":1,"name":"a"}`, false},
		{"false", `{"task":7,"before_software":false}`, false},
		{"true", `{"task":7,"before_software":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var task snapin.Task
			if err := json.Unmarshal([]byte(tc.js), &task); err != nil {
				t.Fatal(err)
			}
			if task.BeforeSoftware != tc.want {
				t.Errorf("BeforeSoftware = %t, want %t", task.BeforeSoftware, tc.want)
			}
		})
	}
}

// reconcile runs the snapins marked before_software, then software, then the
// rest. The fake server records every payload fetch and result in arrival
// order. A snapin payload that does not match its sha512 reports
// hash_mismatch, so each snapin closes with a result and nothing is executed;
// the server's answer (snapinOutcome) decides what the agent does next.
func TestReconcileRunsSnapinsAroundSoftware(t *testing.T) {
	const preID, postID = 7, 8
	pre := snapin.Task{ID: preID, SnapinID: 1, Name: "pre", File: "pre.ps1", SHA512: "00", BeforeSoftware: true}
	post := snapin.Task{ID: postID, SnapinID: 2, Name: "post", File: "post.ps1", SHA512: "00"}
	sw := &software.Policy{Entries: []software.Entry{
		{ID: 3, Backend: "nosuchbackend", Package: "avast", State: "present"},
	}}

	for _, tc := range []struct {
		name                   string
		caps                   []string
		software               *software.Policy
		snapins                []snapin.Task
		preOutcome             string
		wantEvents             []string
		wantApplied            bool
		wantSoftwareWaitingLog bool // the "software: waiting" log line
	}{
		{"pre, software, post", []string{"software", "snapin"}, sw,
			[]snapin.Task{post, pre}, enroll.OutcomeSuccess,
			[]string{"payload 7", "result snapin 7", "result software 3", "payload 8", "result snapin 8"},
			true, false},
		{"snapin listed first still runs pre, software, post", []string{"snapin", "software"}, sw,
			[]snapin.Task{post, pre}, enroll.OutcomeSuccess,
			[]string{"payload 7", "result snapin 7", "result software 3", "payload 8", "result snapin 8"},
			true, false},
		{"pre retry holds software and post", []string{"software", "snapin"}, sw,
			[]snapin.Task{post, pre}, enroll.OutcomeRetry,
			[]string{"payload 7", "result snapin 7"},
			false, true},
		{"pre failed in an aborting job: software runs, post does not", []string{"software", "snapin"}, sw,
			[]snapin.Task{post, func() snapin.Task { p := pre; p.AbortOnFail = true; return p }()}, enroll.OutcomeFailed,
			[]string{"payload 7", "result snapin 7", "result software 3"},
			true, false},
		{"no pre snapin: software, then post", []string{"software", "snapin"}, sw,
			[]snapin.Task{post}, enroll.OutcomeSuccess,
			[]string{"result software 3", "payload 8", "result snapin 8"},
			true, false},
		{"only pre snapins: software after them, nothing left", []string{"software", "snapin"}, sw,
			[]snapin.Task{pre}, enroll.OutcomeSuccess,
			[]string{"payload 7", "result snapin 7", "result software 3"},
			true, false},
		{"software block nil: full queue in server order", []string{"software", "snapin"}, nil,
			[]snapin.Task{post, pre}, enroll.OutcomeSuccess,
			[]string{"payload 8", "result snapin 8", "payload 7", "result snapin 7"},
			true, false},
		{"software not listed: full queue in server order", []string{"snapin"}, sw,
			[]snapin.Task{post, pre}, enroll.OutcomeSuccess,
			[]string{"payload 8", "result snapin 8", "payload 7", "result snapin 7"},
			true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var events []string
			note := func(s string) {
				mu.Lock()
				defer mu.Unlock()
				events = append(events, s)
			}
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/agent/v1/result":
					var req enroll.ResultRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					outcome := enroll.OutcomeSuccess
					if req.Capability == "snapin" && req.Item != nil && req.Item.ID == preID {
						outcome = tc.preOutcome
					}
					if req.Item != nil {
						note(fmt.Sprintf("result %s %d", req.Capability, req.Item.ID))
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "outcome": outcome})
				case strings.HasPrefix(r.URL.Path, "/agent/v1/payload/snapin/"):
					id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/agent/v1/payload/snapin/"))
					note(fmt.Sprintf("payload %d", id))
					_, _ = w.Write([]byte("not the declared bytes"))
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

			desired := &enroll.DesiredState{
				Revision:     "new",
				Capabilities: tc.caps,
				Software:     tc.software,
				Snapins:      tc.snapins,
			}
			err = reconcile(t.Context(), st, client, desired, &sayer{})

			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(events, tc.wantEvents) {
				t.Errorf("server saw\n  %v\nwant\n  %v\nlog:\n%s", events, tc.wantEvents, log.String())
			}
			if applied := err == nil; applied != tc.wantApplied {
				t.Errorf("revision applied = %t (err %v), want %t", applied, err, tc.wantApplied)
			}
			if !tc.wantApplied && st.Config.AppliedRevision != "old" {
				t.Errorf("AppliedRevision = %q, want it unchanged", st.Config.AppliedRevision)
			}
			waiting := strings.Contains(log.String(), "software: waiting; a snapin marked to run before software has not finished")
			if waiting != tc.wantSoftwareWaitingLog {
				t.Errorf("log has the software-waiting line = %t, want %t\n%s", waiting, tc.wantSoftwareWaitingLog, log.String())
			}
		})
	}
}

// The stop_snapins hold covers only the snapins that run after software. A
// failing stop_snapins entry with no post snapin holds nothing, so the
// revision applies; with a post snapin queued, that snapin is held and the
// revision stays unapplied. A before_software snapin runs in both cases.
// The pre phase is also incomplete, and holds software and the post snapins,
// when its payload fetch fails (a 500) or its result POST fails (a 500).
func TestReconcileSnapinPhasesAgainstHoldsAndFailures(t *testing.T) {
	const preID, postID = 7, 8
	pre := snapin.Task{ID: preID, SnapinID: 1, Name: "pre", File: "pre.ps1", SHA512: "00", BeforeSoftware: true}
	post := snapin.Task{ID: postID, SnapinID: 2, Name: "post", File: "post.ps1", SHA512: "00"}

	for _, tc := range []struct {
		name                string
		stopSnapins         bool
		softwareOutcome     string
		payloadStatus       int // for the pre snapin; 0 means 200
		preResultStatus     int // HTTP status of the pre snapin's result POST; 0 means 200
		snapins             []snapin.Task
		wantEvents          []string
		wantApplied         bool
		wantSoftwareWaiting bool
		wantSnapinWaiting   bool
	}{
		{"failing stop_snapins entry, pre only: nothing held", true, enroll.OutcomeFailed, 0, 0,
			[]snapin.Task{pre},
			[]string{"payload 7", "result snapin 7", "result software 3"},
			true, false, false},
		{"failing stop_snapins entry, post queued: post held", true, enroll.OutcomeFailed, 0, 0,
			[]snapin.Task{post, pre},
			[]string{"payload 7", "result snapin 7", "result software 3"},
			false, false, true},
		{"pre payload fetch fails", false, enroll.OutcomeSuccess, http.StatusInternalServerError, 0,
			[]snapin.Task{post, pre},
			[]string{"payload 7"},
			false, true, false},
		{"pre result POST fails", false, enroll.OutcomeSuccess, 0, http.StatusInternalServerError,
			[]snapin.Task{post, pre},
			[]string{"payload 7", "result snapin 7"},
			false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var events []string
			note := func(s string) {
				mu.Lock()
				defer mu.Unlock()
				events = append(events, s)
			}
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/agent/v1/result":
					var req enroll.ResultRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					if req.Item != nil {
						note(fmt.Sprintf("result %s %d", req.Capability, req.Item.ID))
					}
					outcome := enroll.OutcomeSuccess
					if req.Capability == "software" {
						outcome = tc.softwareOutcome
					}
					if req.Capability == "snapin" && req.Item != nil && req.Item.ID == preID && tc.preResultStatus != 0 {
						http.Error(w, "boom", tc.preResultStatus)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "outcome": outcome})
				case strings.HasPrefix(r.URL.Path, "/agent/v1/payload/snapin/"):
					id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/agent/v1/payload/snapin/"))
					note(fmt.Sprintf("payload %d", id))
					if id == preID && tc.payloadStatus != 0 {
						http.Error(w, "boom", tc.payloadStatus)
						return
					}
					_, _ = w.Write([]byte("not the declared bytes"))
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

			desired := &enroll.DesiredState{
				Revision:     "new",
				Capabilities: []string{"software", "snapin"},
				Software: &software.Policy{Entries: []software.Entry{
					{ID: 3, Backend: "nosuchbackend", Package: "avast", State: "present", StopSnapins: tc.stopSnapins},
				}},
				Snapins: tc.snapins,
			}
			err = reconcile(t.Context(), st, client, desired, &sayer{})

			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(events, tc.wantEvents) {
				t.Errorf("server saw\n  %v\nwant\n  %v\nlog:\n%s", events, tc.wantEvents, log.String())
			}
			if applied := err == nil; applied != tc.wantApplied {
				t.Errorf("revision applied = %t (err %v), want %t\nlog:\n%s", applied, err, tc.wantApplied, log.String())
			}
			if !tc.wantApplied && st.Config.AppliedRevision != "old" {
				t.Errorf("AppliedRevision = %q, want it unchanged", st.Config.AppliedRevision)
			}
			if got := strings.Contains(log.String(), "software: waiting; a snapin marked to run before software has not finished"); got != tc.wantSoftwareWaiting {
				t.Errorf("software-waiting line = %t, want %t\n%s", got, tc.wantSoftwareWaiting, log.String())
			}
			if got := strings.Contains(log.String(), "snapin: waiting"); got != tc.wantSnapinWaiting {
				t.Errorf("snapin-waiting line = %t, want %t\n%s", got, tc.wantSnapinWaiting, log.String())
			}
		})
	}
}
