package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/provider/directoryjoin"
	"github.com/FOGProject/fog-agent/internal/secret"
)

// `fog-agent run --once` ends by printing the whole poll response as JSON.
// That is the concrete way a join credential would reach a terminal, a
// scrollback buffer and whatever an admin pasted it into -- so it is worth a
// gate at the level that actually does the printing, not only in the type.
func TestOnceOutputCannotCarryTheJoinCredential(t *testing.T) {
	const pw = "hunter2-correct-horse"
	resp := enroll.PollResponse{
		Status:   "ok",
		Revision: "3f1c9a0b2d4e5f60",
		State: &enroll.DesiredState{
			Revision:     "3f1c9a0b2d4e5f60",
			Capabilities: []string{"directory"},
			Directory: &directoryjoin.Policy{
				Domain: "corp.example.com", Username: `CORP\fogjoin`,
				Password: secret.New(pw),
			},
		},
	}
	b, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), pw) {
		t.Fatalf("--once would print the join credential:\n%s", b)
	}
	// The field is still there, so an admin reading the output can see that
	// a credential was sent and simply is not shown.
	if !strings.Contains(string(b), secret.Redacted) {
		t.Errorf("nothing marks the credential as withheld:\n%s", b)
	}
}

// A build that learns the directory capability must re-run the revision it
// inherited, or a host that should be joined sits unjoined until something
// unrelated moves the revision.
func TestSupportedCapabilitiesNamesDirectory(t *testing.T) {
	if !strings.Contains(supportedCapabilities, "directory") {
		t.Fatal("the directory capability is not in the applied-with stamp")
	}
	if !needsReconcile(enroll.Config{
		AppliedRevision: "abc",
		AppliedWith:     "hostname,taskreboot,power,software,printers,snapin",
	}, "abc") {
		t.Fatal("an upgrade that learned directory treated the revision as applied")
	}
}

// joinTestClient is a client pointed at a server that accepts any result.
func joinTestClient(t *testing.T) *enroll.Client {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","outcome":"joined"}`)
	}))
	t.Cleanup(srv.Close)
	ca := pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: srv.Certificate().Raw,
	})
	c, err := enroll.NewClient(srv.URL, ca)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A join changes the machine's membership, which makes the directory fact
// this agent already sent wrong. The server cannot ask for a fresh one --
// `want_directory` is true only while it holds no hash at all -- so the
// agent has to volunteer it, or the server keeps believing the host
// unjoined until FactsInterval elapses. That interval is an hour and
// DirectoryJoin's retry cooldown is an hour, so the two line up and the
// join credential is sent once more to a machine already in the domain.
//
// The gate is factsDue rather than the field, because setting the field is
// only useful if it actually reopens the collection.
func TestASettledJoinMakesTheFactsDueAgain(t *testing.T) {
	client := joinTestClient(t)
	now := time.Now()
	for _, tc := range []struct {
		status string
		due    bool
	}{
		{directoryjoin.StatusJoined, true},
		{directoryjoin.StatusAlreadyJoined, true},
		// Nothing changed on the machine, so nothing is stale: a failed or
		// refused join must not cost every host a full re-collection.
		{directoryjoin.StatusFailed, false},
		{directoryjoin.StatusRefused, false},
		{directoryjoin.StatusUnsupported, false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			st := &enroll.State{Config: enroll.Config{
				HostID: 105, FactsChecked: now,
			}}
			ok := reportDirectory(context.Background(), st, client,
				"3f1c9a0b2d4e5f60",
				directoryjoin.Report{Status: tc.status}, &sayer{})
			if !ok {
				t.Fatal("the result did not land")
			}
			if got := factsDue(st.Config, now); got != tc.due {
				t.Errorf("factsDue = %t, want %t", got, tc.due)
			}
		})
	}
}
