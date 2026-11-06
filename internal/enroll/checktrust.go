package enroll

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// ErrNoCACertificate is what CheckTrust returns when the stored CA file
// holds no parsable certificate. Callers treat it like a certificate
// verification failure: the stored trust cannot verify any server.
var ErrNoCACertificate = errors.New("enroll: CA bundle contains no certificates")

// CheckTrust makes one TLS handshake to the server in st.Config.ServerURL
// and verifies it with exactly the trust the state settled: the pinned
// bundle, or this machine's trust store for system trust. The name, the
// chain and the clock are all checked, as the real client checks them. It
// returns nil when the server verifies. A verification failure comes back
// as a *tls.CertificateVerificationError (or ErrNoCACertificate), so a
// caller can tell "the trust is stale" from "the server did not answer".
func (st *State) CheckTrust(ctx context.Context) error {
	u, err := url.Parse(st.Config.ServerURL)
	if err != nil {
		return fmt.Errorf("bad server URL %q: %w", st.Config.ServerURL, err)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("bad server URL %q", st.Config.ServerURL)
	}
	var cfg *tls.Config
	if st.Config.SystemTrust {
		cfg = systemTrustConfig(u.Hostname())
	} else {
		if cfg, err = pinnedConfig(st.CA(), u.Hostname()); err != nil {
			return err
		}
	}
	d := &tls.Dialer{Config: cfg}
	conn, err := d.DialContext(ctx, "tcp", hostPort(u))
	if err != nil {
		return err
	}
	return conn.Close()
}

// ForgetTrust drops what the state settled to verify the server with: the
// pinned CA file and the system-trust flag. The next openState then settles
// trust again, as a first install does. The key and the issued certificate
// are not touched.
func (st *State) ForgetTrust() error {
	if err := os.Remove(filepath.Join(st.Dir, caFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	st.Config.SystemTrust = false
	return st.SaveConfig()
}
