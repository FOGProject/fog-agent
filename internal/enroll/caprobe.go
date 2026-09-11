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
	// SystemTrust is set when the server's certificate is not issued by the
	// CA it publishes, but this machine already trusts it for the server's
	// name: a web UI on a public or corporate certificate. There is then no
	// bundle to pin and no fingerprint to compare, and Subject, Issuer and
	// NotAfter describe the server's own certificate rather than a CA.
	SystemTrust bool
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

// ProbeCA finds out what the agent should trust for a FOG server, before
// anything trusts it. There are two answers, tried in this order.
//
// The CA the server publishes, when the server's own certificate is issued
// by it. That is every FOG server on its own certificates. The fetch is the
// one request the agent makes without a trust anchor, because its purpose
// is to find out what the anchor would be: TLS verification is off for it,
// and nothing it returns is trusted. The caller does not act on the result
// until something outside that connection agrees -- an admin comparing
// Fingerprint against the server's web UI, or a deployment script passing
// the fingerprint it already knows. Fetching the anchor over the connection
// it is meant to verify would be trust on first use; the fingerprint check
// is what makes it an out-of-band decision instead.
//
// This machine's trust store, when the server's certificate is not issued by
// the published CA but verifies against the store for the server's name: a
// web UI on a public or corporate certificate. Here the out-of-band decision
// has already been made, by the CA that checked who holds the name, so
// there is nothing for a person to compare. The published CA would be the
// wrong anchor: pinning it leaves the agent unable to verify a single
// connection.
//
// The order matters. A FOG CA in the machine's store -- the legacy client
// put it there -- must still go through the fingerprint, not skip it.
func ProbeCA(ctx context.Context, serverURL string) (*CAProbe, error) {
	base := strings.TrimRight(serverURL, "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%q is not a server address; it should look like https://fog.example.org/fog", serverURL)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("%q is not a server address; it should look like https://fog.example.org/fog", serverURL)
	}

	pinned, pinErr := probePublished(ctx, base, u)
	if pinErr == nil || u.Scheme != "https" {
		return pinned, pinErr
	}
	trusted, sysErr := probeSystemTrust(ctx, base, u)
	if sysErr == nil {
		return trusted, nil
	}
	var verr *tls.CertificateVerificationError
	if !errors.As(sysErr, &verr) {
		// No handshake at all. The published-CA attempt failed the same
		// way, and its error already says so.
		return nil, pinErr
	}
	return nil, fmt.Errorf(
		"%w; and this machine does not trust the certificate %s presents either (%v). "+
			"A web UI on a public or corporate certificate has to be reached by the name on that certificate, "+
			"from a machine that trusts its issuer; otherwise give the installer the CA's file with --ca",
		pinErr, u.Host, verr.Err)
}

// probePublished fetches the CA the server publishes and checks that the
// server's own certificate is issued by it.
func probePublished(ctx context.Context, base string, u *url.URL) (*CAProbe, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// Deliberately unverified: see ProbeCA. The bytes this brings
			// back are shown to a person, never trusted.
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

// probeSystemTrust connects with full verification against this machine's
// trust store: the chain, the clock and the server's name, exactly as a
// system-trust client checks every later connection. Passing it is the
// decision, so nothing about it is relaxed.
func probeSystemTrust(ctx context.Context, base string, u *url.URL) (*CAProbe, error) {
	d := &tls.Dialer{Config: systemTrustConfig(u.Hostname())}
	conn, err := d.DialContext(ctx, "tcp", hostPort(u))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	leaf := conn.(*tls.Conn).ConnectionState().PeerCertificates[0]
	return &CAProbe{
		SystemTrust: true,
		Subject:     leaf.Subject.String(),
		Issuer:      leaf.Issuer.String(),
		NotAfter:    leaf.NotAfter,
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

// hostPort is the address to dial for a server URL, 443 unless it names a
// port.
func hostPort(u *url.URL) string {
	if u.Port() == "" {
		return net.JoinHostPort(u.Hostname(), "443")
	}
	return u.Host
}

// serverChainsTo checks that the certificate the web server presents is
// issued by the published CA. Only the chain matters here, not the clock or
// the hostname: an expired certificate or a server reached by an address
// its certificate does not name is a real problem, but it is not this one,
// and failing the probe on it would send the reader looking in the wrong
// place.
func serverChainsTo(ctx context.Context, u *url.URL, caPEM []byte) error {
	host := hostPort(u)
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
		return fmt.Errorf("%s publishes a CA at %s, but its own certificate is not issued by it (%v)",
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
