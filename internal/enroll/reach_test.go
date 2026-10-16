package enroll

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestPollSaysWhatToCheckWhenTheServerIsDown is a refused connection, made
// for real: a port that was listening and is now closed.
func TestPollSaysWhatToCheckWhenTheServerIsDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	c := newClient("https://"+addr+"/fog", &tls.Config{})
	_, err = c.Poll(context.Background(), PollRequest{})
	want := "the FOG server at " + addr + " refused the connection. The network works; check that the web server is running on it."
	if err == nil || err.Error() != want {
		t.Fatalf("got  %v\nwant %s", err, want)
	}
	if !errors.Is(err, refusedErrno) {
		t.Errorf("the original error is no longer reachable through Unwrap: %#v", err)
	}
}

// TestPollSaysWhatToCheckWhenTheServerDoesNotAnswer is a request that gets
// no reply before the client gives up, made for real.
func TestPollSaysWhatToCheckWhenTheServerDoesNotAnswer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	c := newClient(srv.URL, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test server
	c.HTTP.Timeout = 50 * time.Millisecond
	_, err := c.Poll(context.Background(), PollRequest{})
	host := strings.TrimPrefix(srv.URL, "https://")
	want := "cannot reach the FOG server at " + host + ": it did not answer. Check that this machine is on the network and that the server is up."
	if err == nil || err.Error() != want {
		t.Fatalf("got  %v\nwant %s", err, want)
	}
}

// dialError is the shape net/http hands back for a failed connect:
// *url.Error around *net.OpError around *os.SyscallError around the errno.
func dialError(syscallName string, errno syscall.Errno) error {
	return &url.Error{Op: "Post", URL: "https://10.255.20.1/fog/agent/v1/poll", Err: &net.OpError{
		Op: "dial", Net: "tcp",
		Addr: &net.TCPAddr{IP: net.ParseIP("10.255.20.1"), Port: 443},
		Err:  &os.SyscallError{Syscall: syscallName, Err: errno},
	}}
}

func TestExplainUnreachableNamesTheThingToCheck(t *testing.T) {
	const server = "https://10.255.20.1/fog"
	dns := &url.Error{Op: "Post", URL: "https://fog.lab/fog/agent/v1/poll", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "fog.lab", IsNotFound: true},
	}}
	got := explainUnreachable(dns, "https://fog.lab/fog").Error()
	want := "cannot find the FOG server fog.lab: the name does not resolve. Check this machine's DNS settings and the server address."
	if got != want {
		t.Errorf("DNS:\ngot  %s\nwant %s", got, want)
	}

	got = explainUnreachable(dialError(connectSyscall, noRouteErrno), server).Error()
	want = "cannot reach the FOG server at 10.255.20.1: this machine has no network route to it. Check this machine's network connection."
	if got != want {
		t.Errorf("no route:\ngot  %s\nwant %s", got, want)
	}

	got = explainUnreachable(dialError(connectSyscall, timedOutErrno), server).Error()
	want = "cannot reach the FOG server at 10.255.20.1: it did not answer. Check that this machine is on the network and that the server is up."
	if got != want {
		t.Errorf("connect timed out:\ngot  %s\nwant %s", got, want)
	}
}

// TestExplainUnreachableLeavesOtherErrorsAlone: a canceled context is the
// agent shutting down, and anything unrecognized keeps Go's own words
// rather than a guess.
func TestExplainUnreachableLeavesOtherErrorsAlone(t *testing.T) {
	for _, err := range []error{
		&url.Error{Op: "Post", URL: "https://x/", Err: context.Canceled},
		errors.New("tls: failed to verify certificate"),
	} {
		if got := explainUnreachable(err, "https://x/fog"); got != err {
			t.Errorf("%v was rewritten to %v", err, got)
		}
	}
}
