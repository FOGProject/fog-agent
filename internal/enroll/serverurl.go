package enroll

import (
	"net/url"
	"strings"
)

// HTTPSAddress turns an http:// server address into the https:// address on
// the same host, and reports whether it did.
//
// The server knows an agent only by its TLS client certificate, so nothing
// after enrollment can work over plain HTTP: the server answers every call
// with no_client_certificate. Before this, an http:// address enrolled, got
// a certificate it could never present, and failed every poll (forum topic
// 18320). A FOG install serves HTTPS whether or not it redirects HTTP to it
// (installfog.sh --no-force-https: "serve both HTTP and HTTPS"), so the same
// host on https:// is the address that works.
//
// Only port 80 or no port is moved. Another explicit port is left alone: it
// says where a person put the server, and port 443 is a guess this cannot
// make for them. Such an address still fails, and the log says why.
func HTTPSAddress(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "http") || u.Hostname() == "" {
		return raw, false
	}
	if p := u.Port(); p != "" && p != "80" {
		return raw, false
	}
	u.Scheme = "https"
	u.Host = u.Hostname()
	if strings.Contains(u.Host, ":") {
		// An IPv6 literal needs its brackets back once the port is gone.
		u.Host = "[" + u.Host + "]"
	}
	return u.String(), true
}
