package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/usersession"
)

func sessionState(t *testing.T) *enroll.State {
	t.Helper()
	st, err := enroll.Load(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return st
}

func boolp(b bool) *bool { return &b }

func pending(keys ...string) []usersession.Session {
	var out []usersession.Session
	for _, k := range keys {
		out = append(out, usersession.Session{Key: k, User: "telliott", EndReason: usersession.EndLogout})
	}
	return out
}

// The whole point of holding closures in state: a poll that never landed must
// not consume them. Losing a close means a session that stays open forever,
// which is the legacy defect design 0008 exists to fix.
func TestPendingClosuresSurviveAFailedPoll(t *testing.T) {
	st := sessionState(t)
	st.Config.SessionsPending = pending("2", "3")

	// sent=false is what the caller passes when the block never went.
	if err := recordSessions(st, "", false, &enroll.PollResponse{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(st.Config.SessionsPending) != 2 {
		t.Errorf("pending closures = %d, want 2 kept", len(st.Config.SessionsPending))
	}
}

func TestPendingClosuresClearOnlyAfterAcceptance(t *testing.T) {
	st := sessionState(t)
	st.Config.SessionsPending = pending("2", "3")
	now := time.Now()

	if err := recordSessions(st, "digest-abc", true, &enroll.PollResponse{}, now); err != nil {
		t.Fatal(err)
	}
	if len(st.Config.SessionsPending) != 0 {
		t.Errorf("pending closures = %d, want cleared", len(st.Config.SessionsPending))
	}
	if st.Config.SessionsAcked != "digest-abc" {
		t.Errorf("acked digest = %q, want the one that was sent", st.Config.SessionsAcked)
	}
	if !st.Config.SessionsChecked.Equal(now) {
		t.Errorf("SessionsChecked = %v, want %v", st.Config.SessionsChecked, now)
	}
}

// An old server has never heard of collect_sessions and sends nothing. Absent
// must not read as "the admin turned it off" -- the same distinction
// collect_facts needs, and the reason both are pointers.
func TestAbsentCollectSessionsDoesNotDisable(t *testing.T) {
	st := sessionState(t)
	if err := recordSessions(st, "d", true, &enroll.PollResponse{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st.Config.SessionsDisabled {
		t.Error("a server that said nothing disabled session collection")
	}
}

func TestCollectSessionsFalseDisablesAndDropsQueue(t *testing.T) {
	st := sessionState(t)
	st.Config.SessionsPending = pending("2")
	st.Config.SessionsAcked = "old"

	resp := &enroll.PollResponse{CollectSessions: boolp(false)}
	if err := recordSessions(st, "d", true, resp, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !st.Config.SessionsDisabled {
		t.Error("collect_sessions=false did not disable collection")
	}
	if len(st.Config.SessionsPending) != 0 || st.Config.SessionsAcked != "" {
		t.Error("a disabled agent kept a queued report the server said it did not want")
	}
}

func TestCollectSessionsTrueReenables(t *testing.T) {
	st := sessionState(t)
	st.Config.SessionsDisabled = true

	resp := &enroll.PollResponse{CollectSessions: boolp(true)}
	if err := recordSessions(st, "d", true, resp, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st.Config.SessionsDisabled {
		t.Error("collect_sessions=true did not re-enable collection")
	}
}

// A disabled agent must not even look at who is logged on. attach returning
// early is what enforces that, and it must not touch the request.
func TestAttachSaysNothingWhenDisabled(t *testing.T) {
	st := sessionState(t)
	st.Config.SessionsDisabled = true

	var req enroll.PollRequest
	w := &sessionWatcher{}
	digest, sent := w.attach(st, &req, time.Now())
	if sent || digest != "" {
		t.Errorf("attach reported sent=%v digest=%q while disabled", sent, digest)
	}
	if req.Sessions != nil {
		t.Error("attach put a session block on the request while disabled")
	}
}

// The state file must round-trip a queued closure: it is held across polls
// and, on a service restart mid-outage, across process lifetimes.
func TestPendingClosuresRoundTripThroughState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	st, err := enroll.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Date(2026, 9, 4, 10, 15, 0, 0, time.UTC)
	st.Config.SessionsPending = []usersession.Session{{
		Key: "2", User: "telliott", Domain: "LAB", Type: usersession.TypeRemote,
		RemoteHost: "10.255.25.9", EndedAt: ended, EndReason: usersession.EndDisconnect,
	}}
	if err := st.SaveConfig(); err != nil {
		t.Fatal(err)
	}

	back, err := enroll.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Config.SessionsPending) != 1 {
		t.Fatalf("reloaded %d pending closures, want 1", len(back.Config.SessionsPending))
	}
	got := back.Config.SessionsPending[0]
	if !got.EndedAt.Equal(ended) {
		t.Errorf("EndedAt = %v, want %v", got.EndedAt, ended)
	}
	if got.EndReason != usersession.EndDisconnect || got.RemoteHost != "10.255.25.9" {
		t.Errorf("closure lost detail in the round trip: %+v", got)
	}
}
