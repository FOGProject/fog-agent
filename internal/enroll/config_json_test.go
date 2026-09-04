package enroll

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestZeroTimesAreOmitted pins that a fresh config writes no timestamp keys.
//
// `omitempty` does not omit a zero struct, and time.Time is a struct, so every
// time field tagged that way was written out as "0001-01-01T00:00:00Z". It was
// found on the lab VM, where config.json claimed the agent's run of refused
// polls began in the year 1 -- readable as a real marker by anyone (including
// me) reading the file to work out what the agent thought was going on.
// `omitzero` (Go 1.24+) is the tag that actually consults IsZero.
//
// Behavior did not change: a zero time round-trips through the year-1 string
// back to a zero time, so IsZero() was always right. What was wrong was the
// file, which is the thing a human reads when the agent misbehaves.
func TestZeroTimesAreOmitted(t *testing.T) {
	b, err := json.Marshal(Config{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if strings.Contains(got, "0001-01-01") {
		t.Errorf("a zero config serialized a year-1 timestamp: %s", got)
	}
	for _, key := range []string{
		"unauthorized_since", "facts_checked", "software_checked", "power_fired",
	} {
		if strings.Contains(got, key) {
			t.Errorf("zero config wrote %q; want the key omitted: %s", key, got)
		}
	}
}

// TestSetTimesAreKept is the other half: omitzero must not drop a real value.
func TestSetTimesAreKept(t *testing.T) {
	when := time.Date(2026, 9, 4, 14, 39, 54, 0, time.UTC)
	b, err := json.Marshal(Config{UnauthorizedSince: when, FactsChecked: when})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Config
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back.UnauthorizedSince.Equal(when) {
		t.Errorf("unauthorized_since = %v, want %v", back.UnauthorizedSince, when)
	}
	if !back.FactsChecked.Equal(when) {
		t.Errorf("facts_checked = %v, want %v", back.FactsChecked, when)
	}
}
