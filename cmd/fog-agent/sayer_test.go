package main

import (
	"bytes"
	"strings"
	"testing"
)

// Under the service, logOut is the log file and every line is stamped. A
// repeated state must still print once, not once a poll.
func TestSayerDropsRepeatWhenStamping(t *testing.T) {
	var buf bytes.Buffer
	saved := logOut
	logOut = &buf
	defer func() { logOut = saved }()

	s := &sayer{}
	s.say("directory rejoin: blocked (directory_task_unknown)")
	s.say("directory rejoin: blocked (directory_task_unknown)")
	s.say("directory rejoin: native success")
	s.say("directory rejoin: native success")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d:\n%s", len(lines), buf.String())
	}
	if !strings.HasSuffix(lines[0], " directory rejoin: blocked (directory_task_unknown)") {
		t.Fatalf("first line not stamped as expected: %q", lines[0])
	}
}
