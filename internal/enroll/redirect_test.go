package enroll

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPollDoesNotFollowARedirect reproduces the lab incident of 2026-09-04.
//
// FOG redirects every request to its schema updater while the database is
// mid-upgrade (DatabaseManager::init()), and the redirect it sends is
// RELATIVE: `../management/index.php?node=schema`. An HTTP client that
// follows redirects resolves that against /agent/v1/ and requests
// /agent/management/index.php, which no server has, so the agent saw
//
//	poll: HTTP 404, body is not JSON: File not found.
//
// once every poll interval on every host in the estate. Nothing in that
// sentence names the cause, and the cause is an ordinary upgrade in
// progress.
//
// The server-side half of the fix answers the agent routes with JSON
// instead of a redirect. This half is the agent refusing to follow one at
// all, which is correct independently: a 3xx is never a valid answer to an
// API call, and following it replaces a diagnosable answer with a
// meaningless one.
func TestPollDoesNotFollowARedirect(t *testing.T) {
	caPEM, serverCert, serverKey, _, _ := testCA(t)

	var landedOn []string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		landedOn = append(landedOn, r.URL.Path)
		// Byte for byte what FOG sends, relative path included.
		http.Redirect(w, r, "../management/index.php?node=schema", http.StatusFound)
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{serverCert.Raw}, PrivateKey: serverKey}},
	}
	srv.StartTLS()
	defer srv.Close()

	client, err := NewClient(srv.URL, caPEM)
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Poll(context.Background(), PollRequest{AgentVersion: "t"})
	if err == nil {
		t.Fatal("a redirected poll must be an error, not a result")
	}

	// The whole point: the message has to name where it was sent, or the
	// next person reads a 404 and has nothing to go on.
	if !strings.Contains(err.Error(), "node=schema") {
		t.Errorf("the error must name the redirect target so the cause is"+
			" readable from the log; got: %v", err)
	}

	// And it must not have chased it. One request, not two.
	if len(landedOn) != 1 {
		t.Errorf("the client followed the redirect: requested %v", landedOn)
	}
}
