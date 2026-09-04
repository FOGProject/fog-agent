package secret

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const pw = "hunter2-correct-horse"

// The whole reason the type exists: `fog-agent run --once` prints the poll
// response as JSON, and the state directory is JSON. Neither may carry the
// value.
func TestMarshalNeverCarriesTheValue(t *testing.T) {
	b, err := json.Marshal(struct {
		User string `json:"user"`
		Pass Secret `json:"pass"`
	}{User: "corp\\fogjoin", Pass: New(pw)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), pw) {
		t.Fatalf("the credential is in the JSON: %s", b)
	}
	if !strings.Contains(string(b), Redacted) {
		t.Errorf("nothing marks the field as withheld: %s", b)
	}
}

// It has to arrive from somewhere. Unmarshal is the one direction that
// carries the value.
func TestUnmarshalCarriesTheValue(t *testing.T) {
	var got struct {
		Pass Secret `json:"pass"`
	}
	if err := json.Unmarshal([]byte(`{"pass":"`+pw+`"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Pass.Reveal() != pw {
		t.Fatalf("Reveal = %q", got.Pass.Reveal())
	}
}

// Every log line is one %v away from printing a struct.
func TestEveryFormatVerbRedacts(t *testing.T) {
	s := New(pw)
	holder := struct{ Pass Secret }{Pass: s}
	for _, out := range []string{
		fmt.Sprintf("%s", s),
		fmt.Sprintf("%v", s),
		fmt.Sprintf("%+v", s),
		fmt.Sprintf("%#v", s),
		fmt.Sprintf("%v", holder),
		fmt.Sprintf("%+v", holder),
		// %#v ignores String(), which is why GoString is implemented too.
		fmt.Sprintf("%#v", holder),
		fmt.Sprint(s),
	} {
		if strings.Contains(out, pw) {
			t.Errorf("a format verb printed the credential: %s", out)
		}
	}
}

// An absent credential must not read as a present one: a caller decides
// "nothing to do" from Empty, and a redacted empty would say there was
// something to send.
func TestEmptyIsNotRedacted(t *testing.T) {
	for _, s := range []Secret{{}, New(""), New("   ")} {
		if !s.Empty() {
			t.Errorf("%q is not reported empty", s.Reveal())
		}
		if s.String() != "" {
			t.Errorf("an empty secret printed %q", s.String())
		}
		b, _ := json.Marshal(s)
		if string(b) != `""` {
			t.Errorf("an empty secret marshaled as %s", b)
		}
	}
}

func TestZeroDropsTheValue(t *testing.T) {
	s := New(pw)
	s.Zero()
	if !s.Empty() || s.Reveal() != "" {
		t.Fatalf("Reveal after Zero = %q", s.Reveal())
	}
}
