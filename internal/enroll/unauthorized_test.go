package enroll

import (
	"errors"
	"testing"
)

// The bodies here are the ones a server actually sends. The empty and
// non-JSON cases are not hypothetical: a webroot rolled back to a build
// without the agent routes answered 401 with an empty body on 2026-09-04,
// and the agent discarded a working certificate because of it.
func TestClassifyUnauthorizedOnlyActsOnAnExplicitUnknownCertificate(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want error
	}{
		// The one case that means this agent's binding is gone.
		{"explicit unknown certificate",
			`{"status":"unauthorized","reason":"unknown_certificate","error":"client certificate required"}`,
			ErrCertificateUnknown},

		// Server-side conditions. The certificate may be perfectly good,
		// and re-enrolling would need an admin to approve the host again.
		{"no client certificate reached PHP",
			`{"status":"unauthorized","reason":"no_client_certificate"}`, ErrUnauthorized},
		{"the database was unreachable",
			`{"status":"unauthorized","reason":"server_error"}`, ErrUnauthorized},
		{"two hosts share the fingerprint",
			`{"status":"unauthorized","reason":"ambiguous_certificate"}`, ErrUnauthorized},

		// Anything that cannot be read as a reason. The rolled-back
		// webroot produced the empty case exactly.
		{"empty body, as a rolled-back webroot sends", "", ErrUnauthorized},
		{"html from a proxy", "<html><body>401</body></html>", ErrUnauthorized},
		{"json without a reason", `{"error":"client certificate required"}`, ErrUnauthorized},
		{"a reason we do not know", `{"reason":"something_new"}`, ErrUnauthorized},

		// Near-misses must not be read as the real thing.
		{"reason is a prefix", `{"reason":"unknown_certificate_x"}`, ErrUnauthorized},
		{"reason in the wrong field", `{"error":"unknown_certificate"}`, ErrUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyUnauthorized([]byte(tc.body))
			if !errors.Is(got, tc.want) {
				t.Errorf("classifyUnauthorized(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// The reason travels with a refusal so the log can name it (forum topic
// 18320). Only the server's own shape is kept: the reason is printed.
func TestUnauthorizedReasonCarriesTheServerReason(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"status":"unauthorized","reason":"no_client_certificate"}`, "no_client_certificate"},
		{`{"reason":"server_error"}`, "server_error"},
		{`{"reason":"ambiguous_certificate"}`, "ambiguous_certificate"},
		{`{"reason":"unknown_certificate"}`, ""},
		{"", ""},
		{"<html>401</html>", ""},
		{`{"reason":"bad\nline"}`, ""},
		{`{"reason":"Proxy Says No"}`, ""},
	} {
		err := classifyUnauthorized([]byte(tc.body))
		if got := UnauthorizedReason(err); got != tc.want {
			t.Errorf("UnauthorizedReason(%q) = %q, want %q", tc.body, got, tc.want)
		}
		if tc.want != "" && (!errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrCertificateUnknown)) {
			t.Errorf("%q: a refusal with a reason must still be ErrUnauthorized and only that", tc.body)
		}
	}
}

// The two sentinels must stay distinguishable. If one were defined as
// wrapping the other, errors.Is would match both and the switch in the run
// loop would take whichever arm came first.
func TestTheTwoUnauthorizedSentinelsAreDistinct(t *testing.T) {
	if errors.Is(ErrUnauthorized, ErrCertificateUnknown) {
		t.Error("a plain refusal must not satisfy ErrCertificateUnknown, or it will destroy the identity")
	}
	if errors.Is(ErrCertificateUnknown, ErrUnauthorized) {
		t.Error("the sentinels must not alias, or the run loop cannot tell them apart")
	}
}
