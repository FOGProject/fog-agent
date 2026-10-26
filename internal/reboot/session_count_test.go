package reboot

import (
	"errors"
	"reflect"
	"testing"
)

func TestDecodeWTSUserName(t *testing.T) {
	tests := []struct {
		name      string
		units     []uint16
		byteCount uint32
		want      string
		wantErr   bool
	}{
		{name: "empty username", units: []uint16{0}, byteCount: 2, want: ""},
		{name: "username", units: []uint16{'f', 'o', 'g', 0}, byteCount: 8, want: "fog"},
		{name: "missing terminator", units: []uint16{'f', 'o', 'g'}, byteCount: 6, wantErr: true},
		{name: "embedded terminator", units: []uint16{'f', 0, 'g', 0}, byteCount: 8, wantErr: true},
		{name: "too short", units: nil, byteCount: 0, wantErr: true},
		{name: "odd byte count", units: []uint16{'x'}, byteCount: 3, wantErr: true},
		{name: "byte count mismatch", units: []uint16{'x', 0}, byteCount: 2, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeWTSUserName(tt.units, tt.byteCount)
			if (err != nil) != tt.wantErr {
				t.Fatalf("decodeWTSUserName error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("decodeWTSUserName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCountLoggedInSessionsFixtures(t *testing.T) {
	tests := []struct {
		name      string
		sessions  []session
		usernames map[uint32]string
		want      int
		wantCalls []uint32
	}{
		{
			name: "logged out login screen fixture",
			sessions: []session{
				{id: 0, state: sessionDisconnected},
				{id: 1, state: 1},     // WTSConnected is not counted as Active.
				{id: 65536, state: 6}, // WTSListen.
			},
			want: 0,
		},
		{
			name: "active console and disconnected RDP",
			sessions: []session{
				{id: 1, state: sessionActive},
				{id: 12, state: sessionDisconnected},
			},
			usernames: map[uint32]string{1: "console-user", 12: "rdp-user"},
			want:      2,
			wantCalls: []uint32{1, 12},
		},
		{
			name: "blank successful lookup",
			sessions: []session{
				{id: 4, state: sessionActive},
				{id: 5, state: sessionDisconnected},
			},
			usernames: map[uint32]string{4: "", 5: " \t"},
			want:      1,
			wantCalls: []uint32{4, 5},
		},
		{
			name: "ignored states and session zero are not queried",
			sessions: []session{
				{id: 0, state: sessionActive},
				{id: 7, state: 1},
				{id: 8, state: 2},
				{id: 9, state: 3},
				{id: 10, state: 5},
				{id: 11, state: 6},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []uint32
			got, err := countLoggedInSessions(tt.sessions, func(id uint32) (string, error) {
				calls = append(calls, id)
				return tt.usernames[id], nil
			})
			if err != nil {
				t.Fatalf("countLoggedInSessions: %v", err)
			}
			if got != tt.want {
				t.Fatalf("want %d logged-in sessions, got %d", tt.want, got)
			}
			if !reflect.DeepEqual(calls, tt.wantCalls) {
				t.Fatalf("queried sessions %v, want %v", calls, tt.wantCalls)
			}
		})
	}
}

func TestCountLoggedInSessionsPropagatesQueryError(t *testing.T) {
	wantErr := errors.New("WTS query failed")
	got, err := countLoggedInSessions([]session{
		{id: 3, state: sessionActive},
	}, func(uint32) (string, error) {
		return "", wantErr
	})
	if got != 0 || !errors.Is(err, wantErr) {
		t.Fatalf("want zero and wrapped query error, got count=%d err=%v", got, err)
	}
}

func TestLoggedOutLoginScreenAllowsUnforcedTask(t *testing.T) {
	loggedIn, err := countLoggedInSessions([]session{
		{id: 0, state: sessionDisconnected},
		{id: 1, state: 1},
		{id: 65536, state: 6},
	}, func(uint32) (string, error) {
		t.Fatal("login-screen fixture should not query any session")
		return "", nil
	})
	if err != nil {
		t.Fatalf("count logged-in sessions: %v", err)
	}
	d := Decide([]Reason{{Source: SourceTask, Detail: "task 7"}}, loggedIn, policy)
	if !d.Reboot || d.Delay != 0 {
		t.Fatalf("unforced task on logged-out login screen: got %+v", d)
	}
}
