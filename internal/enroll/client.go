package enroll

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/FOGProject/fog-agent/internal/identity"
	"github.com/FOGProject/fog-agent/internal/provider/hostname"
	"github.com/FOGProject/fog-agent/internal/provider/power"
	"github.com/FOGProject/fog-agent/internal/provider/snapin"
	"github.com/FOGProject/fog-agent/internal/provider/software"
	"github.com/FOGProject/fog-agent/internal/reboot"
)

// Protocol is the agent protocol version this build speaks.
const Protocol = 1

// Request is the enroll body (protocol-v1.md).
type Request struct {
	Protocol     int           `json:"protocol"`
	AgentVersion string        `json:"agent_version"`
	OS           string        `json:"os"`
	Arch         string        `json:"arch"`
	Hostname     string        `json:"hostname"`
	Identity     identity.Host `json:"identity"`
	CSRPEM       string        `json:"csr_pem"`
	Token        string        `json:"token,omitempty"`
}

// Response is the enroll reply. Which fields are set depends on Status.
type Response struct {
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
	HostID     int    `json:"host_id,omitempty"`
	// CertificatePEM is the issued leaf followed by its issuing chain, which
	// is what the agent presents on every later connection.
	CertificatePEM string `json:"certificate_pem,omitempty"`
	NotAfter       string `json:"not_after,omitempty"`
}

// Terminal statuses.
const (
	StatusIssued      = "issued"
	StatusPending     = "pending"
	StatusDenied      = "denied"
	StatusUnsupported = "unsupported"
)

// Client talks to the agent API. It trusts only the CA bundle it was
// given, never the OS store, and after enrollment it presents the issued
// certificate on every connection (design doc 5.2).
type Client struct {
	ServerURL string
	HTTP      *http.Client
	tlsConfig *tls.Config
}

// ErrUnauthorized is returned by Poll when the server answers 401: it did
// not get a certificate it trusts, or the one it got binds to no live host.
// Either way the fix is the same -- drop the certificate and enroll again --
// so the caller gets one sentinel rather than a reason to interpret.
var ErrUnauthorized = errors.New("server does not recognize this agent's certificate")

// PollRequest is the poll body (protocol-v1.md).
type PollRequest struct {
	AgentVersion string `json:"agent_version"`
	// AppliedRevision is the revision this agent last applied, compared
	// by the server for equality only: the desired state rides the answer
	// when it differs. Empty means "send it".
	AppliedRevision string `json:"applied_revision"`
	// WantState asks for the desired state even at the same revision (a
	// software drift check needs the set without the revision moving).
	WantState bool `json:"want_state,omitempty"`
}

// PollResponse is what the server can do for this host right now.
type PollResponse struct {
	Status   string `json:"status"`
	Protocol int    `json:"protocol"`
	Host     struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"host"`
	// Revision is opaque: the agent compares it with the one it applied
	// and never reads anything into it (protocol-v1.md).
	Revision     string `json:"revision"`
	PollInterval int    `json:"poll_interval"`
	ServerTime   string `json:"server_time"`
	// State is the desired state, present when Revision differs from the
	// applied revision the agent sent, or when it asked for it.
	State *DesiredState `json:"state,omitempty"`
}

// DesiredState is what the server wants this host to look like
// (protocol-v1.md). Blocks are present only for the capabilities listed.
type DesiredState struct {
	Revision     string            `json:"revision"`
	Capabilities []string          `json:"capabilities"`
	Hostname     *hostname.Desired `json:"hostname,omitempty"`
	// Task is the FOG task waiting to boot this machine into imaging,
	// present only while one is queued (capability taskreboot).
	Task *reboot.Task `json:"task,omitempty"`
	// Reboot is the policy every reboot obeys; sent with any state.
	Reboot *reboot.Policy `json:"reboot,omitempty"`
	// Snapins is the host's snapin queue in run order (capability snapin).
	Snapins []snapin.Task `json:"snapins,omitempty"`
	// Software is the desired package set and its drift interval
	// (capability software).
	Software *software.Policy `json:"software,omitempty"`
	// Power is the host's shutdown and reboot schedules plus any
	// on-demand action waiting (capability power).
	Power *power.Policy `json:"power,omitempty"`
}

// ResultRequest is what the agent reports for one capability.
type ResultRequest struct {
	Revision   string `json:"revision"`
	Capability string `json:"capability"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	// Item is a report about one server-owned thing under the capability
	// (a snapin task, a software entry): the server keeps the row, reads
	// the exit code against its return-code table and answers an outcome
	// the agent acts on. Without an item the report is one audit line.
	Item *ResultItem `json:"item,omitempty"`
}

// ResultItem identifies the thing reported on and carries what the
// server's report class for the capability reads: the item's own status
// vocabulary, the raw exit code, the output tail, and for software the
// version the host has now.
type ResultItem struct {
	ID               int    `json:"id"`
	Status           string `json:"status"`
	ExitCode         int    `json:"exit_code"`
	InstalledVersion string `json:"installed_version,omitempty"`
	Details          string `json:"details,omitempty"`
}

// NewClient builds a client that verifies the server against caPEM. An
// empty bundle is refused: enrolling against an unverified server would let
// anyone on the path hand the agent a certificate for a server they control.
func NewClient(serverURL string, caPEM []byte) (*Client, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("enroll: CA bundle contains no certificates")
	}
	cfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &Client{
		ServerURL: strings.TrimRight(serverURL, "/"),
		HTTP: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: cfg},
		},
		tlsConfig: cfg,
	}, nil
}

// UseCertificate presents the issued certificate (the leaf and its chain,
// as SaveIssued stored them) with the agent's key on every connection from
// now on. The chain rides along so the server can verify with only the
// root on file; nginx and Apache both accept depth 2.
func (c *Client) UseCertificate(certPEM []byte, key *ecdsa.PrivateKey) error {
	var chain [][]byte
	rest := certPEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			chain = append(chain, block.Bytes)
		}
	}
	if len(chain) == 0 {
		return errors.New("certificate PEM contains no certificates")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return err
	}
	c.tlsConfig.Certificates = []tls.Certificate{{Certificate: chain, PrivateKey: key, Leaf: leaf}}
	// A connection opened before this call carries no certificate and
	// would be reused for the next request; the server would then see an
	// anonymous caller and answer 401. New connections pick up the config.
	if t, ok := c.HTTP.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
	return nil
}

// Poll asks the server what it can do for this host and records the
// check-in. 401 is ErrUnauthorized; anything else unexpected is an error
// with the status in it.
func (c *Client) Poll(ctx context.Context, req PollRequest) (*PollResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ServerURL+"/agent/v1/poll", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	var out PollResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("poll: HTTP %d, body is not JSON: %.200s", resp.StatusCode, raw)
	}
	if resp.StatusCode != http.StatusOK || out.Status != "ok" {
		return nil, fmt.Errorf("poll: HTTP %d with status %q", resp.StatusCode, out.Status)
	}
	return &out, nil
}

// RenewLead is how long before expiry the agent renews. A third of the
// one-year life the server issues, so a machine that is off for a term
// still comes back inside its certificate; an agent that misses the window
// falls through to the 401 path and an admin, which is the right outcome
// for a certificate that actually lapsed.
const RenewLead = 120 * 24 * time.Hour

// RenewDue reports whether the leaf in certPEM expires within RenewLead of
// now. An unparseable certificate is due: renewing it is the only move
// that can improve things.
func RenewDue(certPEM []byte, now time.Time) bool {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return true
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	return now.Add(RenewLead).After(leaf.NotAfter)
}

// Renew asks for a new certificate for the same key, over the connection
// the current certificate authenticates. The reply is the enroll "issued"
// shape and is stored the same way. 401 is ErrUnauthorized, as for Poll.
func (c *Client) Renew(ctx context.Context, csrPEM []byte) (*Response, error) {
	body, err := json.Marshal(map[string]string{"csr_pem": string(csrPEM)})
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ServerURL+"/agent/v1/renew", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	var out struct {
		Response
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("renew: HTTP %d, body is not JSON: %.200s", resp.StatusCode, raw)
	}
	if resp.StatusCode != http.StatusOK || out.Status != StatusIssued {
		return nil, fmt.Errorf("renew: HTTP %d with status %q: %s", resp.StatusCode, out.Status, out.Error)
	}
	return &out.Response, nil
}

// authed does one JSON exchange over the certificate: 401 is
// ErrUnauthorized, a non-JSON body is an error with the status in it, and
// anything else is decoded into out for the caller to judge.
func (c *Client) authed(ctx context.Context, method, path string, in any, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	hr, err := http.NewRequestWithContext(ctx, method, c.ServerURL+path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	hr.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return resp.StatusCode, ErrUnauthorized
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.StatusCode, fmt.Errorf("%s: HTTP %d, body is not JSON: %.200s", path, resp.StatusCode, raw)
	}
	return resp.StatusCode, nil
}

// Result reports what one capability did at one revision, or with an
// item what happened to one thing under it. The outcome is the server's
// reading of an item's exit code; empty for a report without an item.
func (c *Client) Result(ctx context.Context, r ResultRequest) (string, error) {
	var out struct {
		Status  string `json:"status"`
		Outcome string `json:"outcome"`
		Error   string `json:"error"`
	}
	code, err := c.authed(ctx, http.MethodPost, "/agent/v1/result", r, &out)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK || out.Status != "ok" {
		return "", fmt.Errorf("result: HTTP %d with status %q: %s", code, out.Status, out.Error)
	}
	return out.Outcome, nil
}

// Payload streams the bytes behind one thing under a capability into w:
// for "snapin", the file of one task, and fetching it is what marks the
// task in progress on the server. One route for every kind of payload.
func (c *Client) Payload(ctx context.Context, capability string, id int, w io.Writer) error {
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/agent/v1/payload/%s/%d", c.ServerURL, capability, id), nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("snapin file: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// Outcomes the server hands back for a reported snapin task, from the
// snapin's return-code table applied to the raw exit code.
const (
	OutcomeSuccess = "success"
	OutcomeReboot  = "reboot"
	OutcomeRetry   = "retry"
	OutcomeFailed  = "failed"
)

// Enroll sends one request and decodes the reply. Non-JSON or unexpected
// HTTP statuses are errors; the four protocol statuses are returned as data
// so the caller decides what to do with each.
func (c *Client) Enroll(ctx context.Context, req Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ServerURL+"/agent/v1/enroll", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("enroll: HTTP %d, body is not JSON: %.200s", resp.StatusCode, raw)
	}
	switch {
	case resp.StatusCode == 200 && out.Status == StatusIssued,
		resp.StatusCode == 202 && out.Status == StatusPending,
		resp.StatusCode == 403 && out.Status == StatusDenied,
		resp.StatusCode == 426 && out.Status == StatusUnsupported:
		return &out, nil
	}
	return nil, fmt.Errorf("enroll: HTTP %d with status %q", resp.StatusCode, out.Status)
}
