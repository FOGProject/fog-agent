package main

import (
	"encoding/json"
	"strings"
	"testing"

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
