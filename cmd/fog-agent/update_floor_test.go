package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/FOGProject/fog-agent/internal/provider/update"
)

// The downgrade floor must never sit above the oldest release that can
// actually carry a fleet forward, or it forbids the one recovery it exists
// to protect.
//
// A downgrade is the only way back from a build that installs, starts and
// polls perfectly well and then behaves badly (design 0015 sections 9 and
// 11) -- local rollback cannot catch that one, because by every local
// measure the agent is healthy. If the floor excludes every released
// self-updating version, that recovery has nowhere to land.
//
// This went wrong once: the constant read 0.2.0 while 0.2.0 was still the
// expected next release, and self-update then shipped in 0.1.2 -- putting
// the floor above the only version anyone could roll back to.
func TestTheFloorDoesNotExcludeTheFirstSelfUpdatingRelease(t *testing.T) {
	const firstReleaseWithSelfUpdate = "0.1.2"

	cfg := update.Config{
		Current:      "0.9.0", // anything later, so this is a downgrade
		MinDowngrade: firstSelfUpdatingVersion,
		Now:          time.Now, // Run calls this first thing

		// RootCount stays 0 on purpose: the floor is checked BEFORE the
		// signing root, so passing the floor lands on a different
		// refusal. That difference is what this asserts, and it means
		// the test needs no manifest, no network and no keys.
	}
	res, swapped := update.Run(context.Background(),
		update.Desired{Version: firstReleaseWithSelfUpdate}, cfg)

	if swapped {
		t.Fatal("nothing should have been swapped with no signing root")
	}
	if strings.Contains(res.Detail, update.DetailBelowFloor) {
		t.Fatalf("the floor (%s) refuses %s, the first release that shipped "+
			"self-update: rollback recovery has nowhere to land.\ngot: %s",
			firstSelfUpdatingVersion, firstReleaseWithSelfUpdate, res.Detail)
	}
	if !strings.Contains(res.Detail, update.DetailNoSigningRoot) {
		t.Fatalf("expected to get past the floor and stop at the signing-root "+
			"check; got %q", res.Detail)
	}
}
