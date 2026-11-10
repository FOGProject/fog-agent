package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSetupFinished(t *testing.T) {
	cases := []struct {
		state   string
		present bool
		want    bool
	}{
		{"", false, true},
		{"IMAGE_STATE_COMPLETE", true, true},
		{"IMAGE_STATE_UNDEPLOYABLE", true, false},
		{"IMAGE_STATE_GENERALIZE_RESEAL_TO_OOBE", true, false},
		{"IMAGE_STATE_SPECIALIZE_RESEAL_TO_OOBE", true, false},
		{"IMAGE_STATE_SPECIALIZE_RESEAL_TO_AUDIT", true, false},
	}
	for _, c := range cases {
		if got := setupFinished(c.state, c.present); got != c.want {
			t.Errorf("setupFinished(%q, %v) = %v, want %v", c.state, c.present, got, c.want)
		}
	}
}

// A sysprepped machine in OOBE must not get past the wait until setup
// reports complete, and the wait logs its reason once, not once a pass.
func TestWaitForWindowsSetupHoldsUntilComplete(t *testing.T) {
	var buf bytes.Buffer
	saved, savedInterval := logOut, setupPollInterval
	logOut, setupPollInterval = &buf, time.Millisecond
	defer func() { logOut, setupPollInterval = saved, savedInterval }()

	states := []string{
		"IMAGE_STATE_SPECIALIZE_RESEAL_TO_OOBE",
		"IMAGE_STATE_SPECIALIZE_RESEAL_TO_OOBE",
		"IMAGE_STATE_COMPLETE",
	}
	reads := 0
	read := func() (string, bool, error) {
		s := states[reads]
		reads++
		return s, true, nil
	}
	if err := waitForWindowsSetup(context.Background(), read, &sayer{}); err != nil {
		t.Fatal(err)
	}
	if reads != 3 {
		t.Fatalf("returned after %d reads, want 3", reads)
	}
	if n := strings.Count(buf.String(), "Windows setup has not finished"); n != 1 {
		t.Fatalf("logged the wait %d times, want 1:\n%s", n, buf.String())
	}
}

func TestWaitForWindowsSetupStopsOnCancel(t *testing.T) {
	saved := logOut
	logOut = &bytes.Buffer{}
	defer func() { logOut = saved }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	read := func() (string, bool, error) { return "IMAGE_STATE_UNDEPLOYABLE", true, nil }
	if err := waitForWindowsSetup(ctx, read, &sayer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// An unreadable key must not hold the agent forever.
func TestWaitForWindowsSetupReadErrorContinues(t *testing.T) {
	saved := logOut
	logOut = &bytes.Buffer{}
	defer func() { logOut = saved }()

	read := func() (string, bool, error) { return "", false, errors.New("access denied") }
	if err := waitForWindowsSetup(context.Background(), read, &sayer{}); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}
