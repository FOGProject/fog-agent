package enroll

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/FOGProject/fog-agent/internal/identity"
	"github.com/FOGProject/fog-agent/internal/provider/directoryjoin"
)

const DirectoryJoinTargetFeature = "directory-join-target-v1"
const DirectoryTaskOperationsFeature = "directory-task-operations-v1"

// The two errors differ on purpose. Unavailable means the server could not be
// asked now (offline, 5xx, rate limit), so the caller asks again later.
// Unsupported means the server answered and does not offer the features, so
// the caller leaves directory rejoin off. Neither stops the agent.
var ErrDirectoryFeaturesUnavailable = errors.New("directory feature check unavailable")
var ErrDirectoryFeaturesUnsupported = errors.New("server does not advertise directory-join-target-v1 and directory-task-operations-v1; directory rejoin is off")

// ErrIdentityConflict is the server's answer when this machine's identity
// names more than one FOG host.
var ErrIdentityConflict = errors.New("the server found more than one FOG host for this machine's identity")

type IdentityAnswer struct {
	Status   string `json:"status"`
	Identity struct {
		Status          string `json:"status"`
		HostID          int    `json:"host_id"`
		PrincipalHostID int    `json:"principal_host_id"`
	} `json:"identity"`
}

// The feature probe reuses the public enrollment noun over already trusted HTTPS.
func (c *Client) RequireDirectoryFeatures(ctx context.Context) error {
	u, e := url.Parse(c.ServerURL)
	if e != nil || u.Scheme != "https" {
		return errors.New("directory feature check requires trusted HTTPS")
	}
	r, e := http.NewRequestWithContext(ctx, http.MethodGet, c.ServerURL+"/agent/v1/enroll", nil)
	if e != nil {
		return e
	}
	resp, e := c.HTTP.Do(r)
	if e != nil {
		return ErrDirectoryFeaturesUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return ErrDirectoryFeaturesUnavailable
	}
	features := strings.FieldsFunc(resp.Header.Get("X-FOG-Agent-Features"), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	haveIdentity, haveTarget := false, false
	for _, feature := range features {
		haveIdentity = haveIdentity || feature == DirectoryJoinTargetFeature
		haveTarget = haveTarget || feature == DirectoryTaskOperationsFeature
	}
	if resp.StatusCode != 200 || !haveIdentity || !haveTarget {
		return ErrDirectoryFeaturesUnsupported
	}
	return nil
}

func (c *Client) directoryExchange(ctx context.Context, path string, in, out any) (int, error) {
	b, e := json.Marshal(in)
	if e != nil {
		return 0, e
	}
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, c.ServerURL+path, bytes.NewReader(b))
	if e != nil {
		return 0, e
	}
	r.Header.Set("Content-Type", "application/json")
	resp, e := c.HTTP.Do(r)
	if e != nil {
		return 0, errors.New("directory protocol exchange unavailable; do not repeat a mutation")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 409 {
		return resp.StatusCode, errors.New("directory protocol request refused")
	}
	// In particular, never include an unparseable credential response in an error.
	d := json.NewDecoder(io.LimitReader(resp.Body, 65537))
	if e = d.Decode(out); e != nil {
		return resp.StatusCode, errors.New("invalid directory protocol response")
	}
	return resp.StatusCode, nil
}

func (c *Client) CheckIdentity(ctx context.Context, live identity.Host) (IdentityAnswer, error) {
	var out IdentityAnswer
	code, e := c.directoryExchange(ctx, "/agent/v1/identity/check", map[string]any{"identity": live, "features": []string{DirectoryJoinTargetFeature}}, &out)
	if e != nil {
		return out, e
	}
	if out.Identity.PrincipalHostID < 1 {
		return out, errors.New("invalid identity principal")
	}
	if code == 200 && out.Status == "ok" && out.Identity.Status == "matched" && out.Identity.HostID == out.Identity.PrincipalHostID {
		return out, nil
	}
	if code == 409 && out.Status == "identity_blocked" && out.Identity.Status == "mismatch" {
		return out, nil
	}
	// A conflict means this machine's SMBIOS identity and MACs name more
	// than one FOG host. A distinct error lets the caller say what to check
	// instead of "is the server reachable".
	if code == 409 && out.Status == "identity_blocked" && out.Identity.Status == "conflict" {
		return out, ErrIdentityConflict
	}
	return out, errors.New("the server could not place this machine's identity; the agent keeps working without directory rejoin")
}

func (c *Client) StartRejoin(ctx context.Context, p directoryjoin.RejoinPlan) (*directoryjoin.Policy, error) {
	if p.Version != 2 {
		return nil, errors.New("unsupported directory rejoin plan version; check that the FOG server and the FOG Agent are both current")
	}
	if p.TaskID < 0 {
		return nil, errors.New("invalid rejoin plan task binding")
	}
	var guid any
	if p.Target != nil {
		guid = p.Target.GUID
	}
	var raw json.RawMessage
	body := map[string]any{"attempt_id": p.AttemptID, "config_revision": p.ConfigRevision,
		"target_guid": guid, "task_id": p.TaskID, "operation": p.Operation}
	code, e := c.directoryExchange(ctx, "/agent/v1/directory/rejoin/start", body, &raw)
	var out struct {
		Status    string                `json:"status"`
		Directory *directoryjoin.Policy `json:"directory"`
	}
	if e == nil {
		e = json.Unmarshal(raw, &out)
	}
	if e != nil || code != 200 || out.Status != "ok" || out.Directory == nil || out.Directory.Rejoin == nil {
		if out.Directory != nil {
			out.Directory.Password.Zero()
		}
		return nil, errors.New("rejoin start not confirmed; mutation must not be repeated")
	}
	got := out.Directory.Rejoin
	var envelope struct {
		Directory struct {
			Rejoin json.RawMessage `json:"rejoin"`
		} `json:"directory"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		out.Directory.Password.Zero()
		return nil, errors.New("rejoin response binding changed")
	}
	gotTaskID, taskErr := taskIDFromJSON(envelope.Directory.Rejoin)
	if taskErr != nil || gotTaskID != p.TaskID || got.TaskID != p.TaskID {
		out.Directory.Password.Zero()
		return nil, errors.New("rejoin operation binding changed")
	}
	if got.Version != p.Version || got.Operation != p.Operation || got.ExpectedCurrent != p.ExpectedCurrent || got.ExpectedPending != p.ExpectedPending || got.RetryAuthorized != p.RetryAuthorized {
		out.Directory.Password.Zero()
		return nil, errors.New("rejoin operation binding changed")
	}
	if got.HostID != p.HostID || got.AttemptID != p.AttemptID || got.ConfigRevision != p.ConfigRevision || got.Mode != p.Mode || got.Name != p.Name || got.Domain != p.Domain || got.DC != p.DC || (got.Target == nil) != (p.Target == nil) || (p.Target != nil && (got.Target.GUID != p.Target.GUID || got.Target.SID != p.Target.SID)) {
		out.Directory.Password.Zero()
		return nil, errors.New("rejoin response binding changed")
	}
	return out.Directory, nil
}

func taskIDFromJSON(raw []byte) (int, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return 0, err
	}
	value, ok := fields["task_id"]
	if !ok || len(value) == 0 || value[0] < '0' || value[0] > '9' {
		return 0, errors.New("task_id missing")
	}
	var id int
	if err := json.Unmarshal(value, &id); err != nil || id < 0 {
		return 0, errors.New("task_id must be a non-negative integer")
	}
	return id, nil
}

func (st *State) KeyFingerprint() (string, error) {
	if st.Key == nil {
		return "", errors.New("no agent key")
	}
	der, e := x509.MarshalPKIXPublicKey(&st.Key.PublicKey)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	return hex.EncodeToString(sum[:]), nil
}
func (st *State) RememberIdentity(live identity.Host) error {
	b, e := json.Marshal(live)
	if e != nil {
		return e
	}
	if e = writeFile(filepath.Join(st.Dir, identityFile), b, 0600); e == nil {
		st.Identity = &live
	}
	return e
}

// Explicit local rotation only, never a response to an ambiguous identity or timeout.
func (st *State) RotateIdentity(live identity.Host) error {
	// Only a terminal marker may leave the active slot during identity
	// rotation. Keep its bytes as history, including an earlier key's marker
	// retained by a previous enrollment. Uncertain operations stay active.
	// An unreadable marker is not terminal, so it stays where it is. It must
	// not block the rotation either: a clone that cannot rotate cannot
	// re-enroll, and the service would exit at every start.
	a, e := directoryjoin.LoadAttempt(st.Dir)
	if e == nil && a != nil && a.Phase == "verified" {
		if e = directoryjoin.ArchiveVerifiedAttempt(st.Dir, a); e != nil {
			return e
		}
	}
	config := Config{ServerURL: st.Config.ServerURL, SystemTrust: st.Config.SystemTrust, PendingToken: st.Config.PendingToken}
	st.Identity = nil
	if _, e := st.EnsureKey(live); e != nil {
		return e
	}
	st.Config = config
	if e := st.SaveConfig(); e != nil {
		return e
	}
	return nil
}
