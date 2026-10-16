package enroll

import (
	"errors"
	"net"
	"net/url"
	"syscall"
)

// unreachableError is a failure to talk to the server at all, said in the
// words an admin acts on: what failed and what to check. Go's own text for
// the same failure, as the agent logged it on telliottwin11 on 2026-10-16
// while the machine had no IP address (a self-assigned 169.254 one), was
//
//	poll: Post "https://10.255.20.1/fog/agent/v1/poll": dial tcp
//	10.255.20.1:443: connectex: A connection attempt failed because the
//	connected party did not properly respond after a period of time, or
//	established connection failed because connected host has failed to
//	respond.
//
// Nothing in that says "check this machine's network". The original error
// stays reachable through Unwrap for any caller that tests it.
type unreachableError struct {
	msg string
	err error
}

func (e *unreachableError) Error() string { return e.msg }
func (e *unreachableError) Unwrap() error { return e.err }

// explainUnreachable turns an HTTP transport error into an
// unreachableError when it is one of the ways a server can be out of
// reach, and returns it unchanged otherwise (a TLS failure, a canceled
// context, anything this does not recognize).
func explainUnreachable(err error, target string) error {
	host := target
	if u, perr := url.Parse(target); perr == nil && u.Host != "" {
		host = u.Host
	}
	var dnsErr *net.DNSError
	var errno syscall.Errno
	msg := ""
	switch {
	case errors.As(err, &dnsErr):
		msg = "cannot find the FOG server " + host +
			": the name does not resolve. Check this machine's DNS settings and the server address."
	case errors.As(err, &errno) && isRefused(errno):
		msg = "the FOG server at " + host +
			" refused the connection. The network works; check that the web server is running on it."
	case errors.As(err, &errno) && isNoRoute(errno):
		msg = "cannot reach the FOG server at " + host +
			": this machine has no network route to it. Check this machine's network connection."
	case isTimeout(err):
		msg = "cannot reach the FOG server at " + host +
			": it did not answer. Check that this machine is on the network and that the server is up."
	default:
		return err
	}
	return &unreachableError{msg: msg, err: err}
}

// isTimeout is a dial or request that got no answer: http.Client.Timeout
// expiring, or the OS giving up on the connect itself.
func isTimeout(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) && isTimedOut(errno) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
