// Package secret holds a string that must not end up anywhere it can be
// read later.
//
// It exists because of one concrete way a credential leaks in this agent.
// `fog-agent run --once` prints the whole poll response as JSON, the state
// dir is written as JSON, and every log line is a format string away from
// %v on a struct. A plain string field on the desired state would be
// readable in all three, and a domain join credential is exactly the field
// that must be in none of them (design 0009 §6: held in memory only, never
// written to config.json, never logged at any level).
//
// So the type is deliberately asymmetric. It UNMARSHALS the real value,
// because the value has to arrive from the server. It MARSHALS a
// placeholder, and prints one under every fmt verb, so the only way to get
// the value back out is to ask for it by name -- which is greppable, and
// which is the point.
package secret

import (
	"encoding/json"
	"strings"
)

// Redacted is what a Secret shows instead of itself.
const Redacted = "[redacted]"

// Secret is a string that redacts itself when marshaled, formatted or
// printed. Reveal is the only way to the value.
type Secret struct {
	value string
}

// New wraps a value.
func New(v string) Secret { return Secret{value: v} }

// Reveal returns the value. Named to be conspicuous: a reader looking for
// where a credential is used greps for this and finds every place.
func (s Secret) Reveal() string { return s.value }

// Empty says whether there is a value, without exposing it -- so a caller
// can decide "nothing to do" without ever touching the string.
func (s Secret) Empty() bool { return strings.TrimSpace(s.value) == "" }

// Zero overwrites the value. Go strings are immutable and the garbage
// collector may already have copied the bytes elsewhere, so this is a
// narrowing and not a guarantee: it drops this reference, which is what
// stops the value being marshaled or printed by something that still holds
// the struct after the attempt.
func (s *Secret) Zero() { s.value = "" }

// UnmarshalJSON reads the real value. The one direction that carries it.
func (s *Secret) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	s.value = v
	return nil
}

// MarshalJSON writes the placeholder, never the value. A Secret that has
// been round-tripped through JSON is therefore GONE, which is correct: the
// only reason to marshal one is to print or persist it, and neither may
// have it.
func (s Secret) MarshalJSON() ([]byte, error) {
	if s.Empty() {
		return []byte(`""`), nil
	}
	return json.Marshal(Redacted)
}

// String satisfies fmt.Stringer so %s and %v redact.
func (s Secret) String() string {
	if s.Empty() {
		return ""
	}
	return Redacted
}

// GoString satisfies fmt.GoStringer so %#v redacts too -- that verb ignores
// String() and would otherwise print the struct field verbatim.
func (s Secret) GoString() string { return `secret.Secret{` + s.String() + `}` }
