package main

import (
	"slices"
	"testing"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

// The poll request declares the software-backends feature, with the
// directory context on or off, so the server may send non-choco entries.
func TestPollRequestDeclaresSoftwareBackends(t *testing.T) {
	st := &enroll.State{}
	for _, skip := range []bool{true, false} {
		req := newPollRequest(st, skip)
		if !slices.Contains(req.Features, enroll.SoftwareBackendsFeature) {
			t.Errorf("skipDirectory=%v: features %q lack %q", skip, req.Features, enroll.SoftwareBackendsFeature)
		}
	}
}
