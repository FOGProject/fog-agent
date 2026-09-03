// Package enroll owns the agent's local identity material: the private key,
// the SMBIOS identity it was generated for, and the certificate the server
// issued. It also performs the enrollment request (docs/design/protocol-v1.md).
package enroll

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/FOGProject/fog-agent/internal/identity"
	"github.com/FOGProject/fog-agent/internal/reboot"
)

// Files inside the state directory.
const (
	keyFile      = "key.pem"       // EC P-256 private key, 0600
	identityFile = "identity.json" // SMBIOS identity the key was generated for
	certFile     = "cert.pem"      // issued certificate, empty until enrolled
	caFile       = "ca.pem"        // CA bundle the agent trusts for its server
	configFile   = "config.json"   // server URL and host id
)

// Config is the small amount of local configuration; everything else comes
// from the server (design doc 10.3).
type Config struct {
	ServerURL string `json:"server_url"`
	HostID    int    `json:"host_id,omitempty"`
	// AppliedRevision is the desired-state revision every capability last
	// converged on without failing; a poll reporting a different one is
	// what triggers a fetch. Empty until the first reconcile.
	AppliedRevision string `json:"applied_revision,omitempty"`
	// PendingReboot is what the reboot coordinator still owes: reasons
	// recorded by providers and the task poll that policy has not yet
	// let it act on. Persisted so a restart does not forget them.
	PendingReboot []reboot.Reason `json:"pending_reboot,omitempty"`
	// RebootGrace is the server's FOG_GRACE_TIMEOUT as of the last
	// reconcile, kept so a deferred reboot uses it on a later poll.
	RebootGrace int `json:"reboot_grace,omitempty"`
	// RebootedForTask is the FOG task this agent last rebooted for. A
	// machine that came back with the same task still queued did not
	// boot into FOS, and rebooting it again every poll fixes nothing;
	// a new task (a new id) is a new request.
	RebootedForTask int `json:"rebooted_for_task,omitempty"`
	// SoftwareChecked is when the software set was last converged. The
	// drift check re-runs it after the server's interval even when the
	// revision has not moved.
	SoftwareChecked time.Time `json:"software_checked,omitempty"`
	// SoftwareDrift is the server's drift interval in seconds as of the
	// last reconcile; zero when the host has no software set.
	SoftwareDrift int `json:"software_drift,omitempty"`
}

// State is the on-disk material. Load never generates anything by itself;
// callers decide when a key should exist.
type State struct {
	Dir      string
	Key      *ecdsa.PrivateKey
	Identity *identity.Host // identity recorded at key generation, nil if none
	Cert     []byte         // PEM, nil until issued
	Config   Config
}

// Load reads whatever exists in dir. A missing key is not an error; a key
// that cannot be parsed is.
func Load(dir string) (*State, error) {
	// Created here, not in EnsureKey, so whatever is written first -- the
	// CA bundle, the config, the key -- finds the directory. `run` writes
	// the bundle before it ever touches the key.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	st := &State{Dir: dir}
	if b, err := os.ReadFile(filepath.Join(dir, configFile)); err == nil {
		if err := json.Unmarshal(b, &st.Config); err != nil {
			return nil, fmt.Errorf("enroll: %s: %w", configFile, err)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, keyFile))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "EC PRIVATE KEY" {
		return nil, fmt.Errorf("enroll: %s is not an EC private key", keyFile)
	}
	if st.Key, err = x509.ParseECPrivateKey(blk.Bytes); err != nil {
		return nil, fmt.Errorf("enroll: %s: %w", keyFile, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, identityFile)); err == nil {
		var id identity.Host
		if err := json.Unmarshal(b, &id); err == nil {
			st.Identity = &id
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, certFile)); err == nil && len(b) > 0 {
		st.Cert = b
	}
	return st, nil
}

// EnsureKey returns the existing key if it was generated for the live
// identity, and otherwise generates a fresh one and forgets any certificate.
// This is the clone and reimage guard (design doc 4.4): a captured image
// carries the original machine's key, and the SMBIOS tuple is how the copy
// finds out it is not that machine. The live identity is always recorded so
// the next start compares against what was actually presented.
func (st *State) EnsureKey(live identity.Host) (regenerated bool, err error) {
	if st.Key != nil && st.Identity != nil && sameMachine(*st.Identity, live) {
		return false, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return false, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(st.Dir, 0o700); err != nil {
		return false, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := writeFile(filepath.Join(st.Dir, keyFile), pemBytes, 0o600); err != nil {
		return false, err
	}
	idBytes, _ := json.Marshal(live)
	if err := writeFile(filepath.Join(st.Dir, identityFile), idBytes, 0o600); err != nil {
		return false, err
	}
	// A new key means any old certificate is for a key we no longer hold.
	_ = os.Remove(filepath.Join(st.Dir, certFile))
	st.Key, st.Identity, st.Cert = key, &live, nil
	return true, nil
}

// sameMachine compares only the SMBIOS tuple. MACs change with docks and
// USB adapters and must not trigger a re-enroll. Two hosts whose tuple is
// entirely empty or placeholder (some VMs) compare equal here; the server's
// resolver, not this guard, is what separates those.
func sameMachine(a, b identity.Host) bool {
	return a.SystemUUID == b.SystemUUID &&
		a.SystemSerial == b.SystemSerial &&
		a.BoardSerial == b.BoardSerial &&
		a.ChassisAsset == b.ChassisAsset
}

// CSR builds a certificate request for the key. The subject is a
// placeholder: the server sets the real one on issue (protocol-v1.md).
func (st *State) CSR() ([]byte, error) {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, st.Key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// SaveIssued stores the certificate and the host id the server assigned.
func (st *State) SaveIssued(certPEM []byte, hostID int) error {
	if err := writeFile(filepath.Join(st.Dir, certFile), certPEM, 0o600); err != nil {
		return err
	}
	st.Cert = certPEM
	st.Config.HostID = hostID
	return st.SaveConfig()
}

// DropIssued forgets the certificate and host id, keeping the key: the
// next enrollment presents the same key, which is what lets the server
// recognize a host it still has ("reissue") or re-pend one it does not.
func (st *State) DropIssued() error {
	if err := os.Remove(filepath.Join(st.Dir, certFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	st.Cert = nil
	st.Config.HostID = 0
	return st.SaveConfig()
}

// SaveConfig writes config.json.
func (st *State) SaveConfig() error {
	b, _ := json.MarshalIndent(st.Config, "", "  ")
	return writeFile(filepath.Join(st.Dir, configFile), b, 0o600)
}

// SaveCA stores the server CA bundle the agent will trust.
func (st *State) SaveCA(pemBytes []byte) error {
	return writeFile(filepath.Join(st.Dir, caFile), pemBytes, 0o600)
}

// CA returns the stored bundle, or nil if none.
func (st *State) CA() []byte {
	b, _ := os.ReadFile(filepath.Join(st.Dir, caFile))
	return b
}

// writeFile writes atomically (temp file plus rename) so a crash mid-write
// cannot leave a truncated key or certificate.
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
