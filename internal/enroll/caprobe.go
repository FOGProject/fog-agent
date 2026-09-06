package enroll

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// caPublishedPath is where a FOG server publishes the CA it signs its own
// certificate with, next to the DER copy of the same thing.
const caPublishedPath = "/management/other/ca.cert.pem"

// CAProbe is what the server says about the certificate authority it wants
// to be trusted with, gathered before anything trusts it.
type CAProbe struct {
	// PEM is the bundle to pin, exactly as the server published it.
	PEM []byte
	// Fingerprint is SHA-256 over the CA certificate's DER, formatted the
	// way `openssl x509 -fingerprint -sha256` prints it (upper-case hex,
	// colon-separated), so an admin can compare it character by character
	// against what the FOG web UI shows.
	Fingerprint string
	Subject     string
	Issuer      string
	NotAfter    time.Time
	// ServerURL is the address the probe was made against, cleaned up.
	ServerURL string
}

// ProbeCA fetches the CA a FOG server publishes and checks that the server's
// own certificate is actually issued by it.
//
// This is the one request the agent makes without a trust anchor, because
// its whole purpose is to find out what the anchor would be: TLS
// verification is off for it, and nothing it returns is trusted. What makes
// it safe is that the caller does not act on the result -- an admin
// compares Fingerprint against what the server's web UI displays, or a
// deployment script passes the fingerprint it already knows, and only a
// match settles the trust. Fetching the anchor over the connection it is
// meant to verify would be trust on first use; the fingerprint check is
// what makes it an out-of-band decision instead.
//
// The chain check is the part that catches the case nobody thinks about: a
// FOG server whose web UI runs on a public or corporate certificate still
// publishes its own internal CA at the same path, and pinning that CA would
// leave the agent unable to verify a single connection. The error says so
// in those words rather than failing later as a handshake error.
func ProbeCA(ctx context.Context, serverURL string) (*CAProbe, error) {
	base := strings.TrimRight(serverURL, "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%q is not a server address; it should look like https://fog.example.org/fog", serverURL)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("%q is not a server address; it should look like https://fog.example.org/fog", serverURL)
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// Deliberately unverified: see the comment above. The bytes
			// this brings back are shown to a person, never trusted.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec
		},
		CheckRedirect: refuseRedirect,
	}
	defer client.CloseIdleConnections()

	caPEM, err := fetch(ctx, client, base+caPublishedPath)
	if err != nil {
		return nil, fmt.Errorf("could not read the certificate the server publishes at %s: %w", caPublishedPath, err)
	}
	ca, err := firstCertificate(caPEM)
	if err != nil {
		return nil, fmt.Errorf("%s%s did not answer with a certificate: %w", base, caPublishedPath, err)
	}

	if u.Scheme == "https" {
		if err := serverChainsTo(ctx, u, caPEM); err != nil {
			return nil, err
		}
	}

	sum := sha256.Sum256(ca.Raw)
	return &CAProbe{
		PEM:         caPEM,
		Fingerprint: FormatFingerprint(sum[:]),
		Subject:     ca.Subject.String(),
		Issuer:      ca.Issuer.String(),
		NotAfter:    ca.NotAfter,
		ServerURL:   base,
	}, nil
}

// FingerprintOf is the same value ProbeCA reports, for a bundle already on
// disk, so `ca probe` and a hand-supplied file can be compared.
func FingerprintOf(caPEM []byte) (string, error) {
	ca, err := firstCertificate(caPEM)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(ca.Raw)
	return FormatFingerprint(sum[:]), nil
}

// FormatFingerprint prints a digest the way every certificate viewer does,
// which is the point: the admin is comparing two strings by eye.
func FormatFingerprint(sum []byte) string {
	var b strings.Builder
	for i, x := range sum {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%02X", x)
	}
	return b.String()
}

// SameFingerprint compares two fingerprints the way a person pasting one
// would want: case and separators do not matter, the digest does.
func SameFingerprint(a, b string) bool {
	clean := func(s string) string {
		return strings.ToUpper(strings.NewReplacer(":", "", " ", "", "-", "").Replace(s))
	}
	ca, cb := clean(a), clean(b)
	return ca != "" && ca == cb
}

// serverChainsTo checks that the certificate the web server presents is
// issued by the published CA. Only the chain matters here, not the clock or
// the hostname: an expired certificate or a server reached by an address
// its certificate does not name is a real problem, but it is not this one,
// and failing the probe on it would send the reader looking in the wrong
// place.
func serverChainsTo(ctx context.Context, u *url.URL, caPEM []byte) error {
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	d := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}} //nolint:gosec
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", host, err)
	}
	defer conn.Close()
	state := conn.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return fmt.Errorf("%s presented no certificate", host)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return errors.New("the published file holds no certificate")
	}
	inter := x509.NewCertPool()
	for _, c := range state.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	_, err = state.PeerCertificates[0].Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inter,
		CurrentTime:   state.PeerCertificates[0].NotBefore,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil {
		return fmt.Errorf(
			"%s publishes a CA at %s, but its own certificate is not issued by it (%v). "+
				"That is what a server whose web UI uses a public or corporate certificate looks like: "+
				"give the installer that certificate authority's file instead, with --ca",
			u.Host, caPublishedPath, err)
	}
	return nil
}

// fetch reads one URL, with a cap so a server answering with something
// enormous cannot be used to exhaust memory before anything has been
// verified.
func fetch(ctx context.Context, c *http.Client, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// firstCertificate returns the leading certificate of a PEM bundle, which
// for a published CA file is the CA itself.
func firstCertificate(bundle []byte) (*x509.Certificate, error) {
	rest := bundle
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, errors.New("no PEM certificate in the answer")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}
