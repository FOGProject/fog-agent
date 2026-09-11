package update

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/FOGProject/fog-agent/internal/provider"
	"github.com/FOGProject/fog-agent/internal/release"
)

// Desired is the update block of the desired state: which version this
// host should be running, and optionally where to look for the signed
// manifest that says what that version is.
//
// An empty ManifestURL means the one this build was compiled with. The
// URL is safe to accept from the server because nothing about it is
// trusted: a hostile server can point the agent at a mirror that serves
// nothing, serves an old manifest (refused by the sequence floor), or
// serves the real thing. There is no fourth option, and that is the whole
// reason this field can exist at all.
type Desired struct {
	Version     string `json:"desired"`
	ManifestURL string `json:"manifest_url"`
	// Manifest and Signature are the signed manifest and its detached
	// envelope: base64 of the exact bytes the server downloaded. A server
	// that syncs releases sends them, so a host with no internet access
	// can still verify. They get no more trust than a mirror does: every
	// check below runs on them unchanged. Used only as a pair.
	Manifest  string `json:"manifest,omitempty"`
	Signature string `json:"signature,omitempty"`
	// Artifact is the payload id of the server's copy of this host's
	// file, fetched from /agent/v1/payload/update/{id}. Zero means the
	// server holds none. The copy is verified like any other, and a copy
	// that fails falls back once to the URL the manifest names.
	Artifact int `json:"artifact,omitempty"`
}

// Details reported alongside a failure, so the server can say which check
// refused rather than "update failed". These are the vocabulary design
// 0015 section 3.6 names; the server stores them and the host list
// filters on them.
const (
	DetailBadVersion    = "bad_desired_version"
	DetailNoSigningRoot = "no_signing_root"
	DetailBelowFloor    = "below_floor"
	DetailSignature     = "signature_invalid"
	DetailStale         = "stale_manifest"
	DetailNoArtifact    = "no_artifact"
	DetailHash          = "hash_mismatch"
	DetailCannotArm     = "cannot_arm_rollback"
	DetailFetch         = "fetch_failed"
	DetailSwap          = "swap_failed"
)

// Config is everything the provider needs that does not come from the
// server. Injected rather than read, because a package that decides
// whether to replace the running binary has to be testable without
// replacing the running binary.
type Config struct {
	// Current is this build's version (main.Version).
	Current string
	// GOOS and GOARCH select the artifact. Passed rather than read from
	// runtime so a test can ask for a platform it is not on.
	GOOS, GOARCH string

	// Roots and RootCount come from release.Roots(). A build with no
	// root cannot verify anything, and says so once instead of failing a
	// signature check every poll.
	Roots     *x509.CertPool
	RootCount int

	// DefaultManifestURL is what this build looks at when the server
	// names no mirror.
	DefaultManifestURL string

	// MinDowngrade is the oldest version this build is willing to become.
	// A downgrade is the only fleet-wide recovery from a build that runs
	// and polls perfectly well and behaves badly, so it is supported --
	// but not below the point where an older agent could not read the
	// state this one wrote.
	MinDowngrade string

	// ExePath is the binary to replace; StateDir is where the download
	// is staged and the probation record kept.
	ExePath  string
	StateDir string

	// Busy reports that something is mid-flight which must not be
	// interrupted: a snapin payload running, a package install, a
	// directory join. The agent replacing itself kills the child, and
	// the server-side row for that work is then stranded in progress
	// with no result ever reported.
	Busy func() (string, bool)

	// Fetch retrieves a URL. Injected so the trust decision for the
	// transport lives with the caller that already owns an HTTP client,
	// and so tests need no network.
	Fetch func(ctx context.Context, url string) (io.ReadCloser, error)

	// Payload streams the server's copy of an artifact over the agent's
	// authenticated connection: the payload route, capability "update".
	// Nil when there is no server to ask, as for `fog-agent update` run by
	// hand, and then only the manifest's own URL is used.
	Payload func(ctx context.Context, id int, w io.Writer) error

	// Now is the clock, for the certificate and manifest validity
	// windows and for the probation deadline.
	Now func() time.Time

	// Probation is how long the new binary has to complete one
	// successful poll before it is reverted.
	Probation time.Duration
}

// maxManifest bounds what will be read from a URL before the signature
// has been checked. Everything downstream of the fetch is untrusted, so
// the size limit comes before the trust, not after it.
const maxManifest = 1 << 20

// Run works out what the server's desired version means for this host and
// carries it out, up to and including replacing the binary. It does not
// restart: the caller exits non-zero and lets the service manager start
// the new binary, which is the same mechanism that recovers from a new
// binary that crashes.
//
// A true second return value means the binary on disk has been replaced
// and the process should now exit so it can be restarted.
func Run(ctx context.Context, d Desired, cfg Config) (provider.Result, bool) {
	now := cfg.Now()

	if !Valid(d.Version) {
		// Includes "latest": delegating the choice to whatever central
		// published this morning is the fog-client defect this design
		// exists to not repeat (design 0015 section 2.2).
		return failed(fmt.Sprintf("%s: %q", DetailBadVersion, d.Version)), false
	}
	switch Compare(cfg.Current, d.Version) {
	case 0:
		return provider.Result{Status: provider.StatusUnchanged,
			Detail: "already " + cfg.Current}, false
	case 1:
		if cfg.MinDowngrade != "" && Compare(d.Version, cfg.MinDowngrade) < 0 {
			return failed(fmt.Sprintf("%s: %s is below this build's floor of %s",
				DetailBelowFloor, d.Version, cfg.MinDowngrade)), false
		}
	}
	if cfg.RootCount == 0 {
		// Nothing could ever verify, so this is a fact about the build
		// rather than a failure of this attempt.
		return failed(DetailNoSigningRoot + ": this build carries no release signing root"), false
	}
	if cfg.Busy != nil {
		if what, busy := cfg.Busy(); busy {
			// Not a failure and not a refusal: the revision stays
			// unapplied and the next poll tries again.
			return provider.Result{Status: provider.StatusUnchanged,
				Detail: "deferred, " + what + " in flight"}, false
		}
	}

	// The pair the server sent, when it sent a usable one, is checked
	// exactly as a download would be. Otherwise ask the manifest URL.
	body, envRaw, ok := inline(d)
	if !ok {
		url := d.ManifestURL
		if url == "" {
			url = cfg.DefaultManifestURL
		}
		if url == "" {
			return failed(DetailNoArtifact + ": no manifest url"), false
		}
		var err error
		if body, err = fetchLimited(ctx, cfg, url, maxManifest); err != nil {
			return failed(DetailFetch + ": manifest: " + err.Error()), false
		}
		if envRaw, err = fetchLimited(ctx, cfg, url+".sig", maxManifest); err != nil {
			return failed(DetailFetch + ": signature: " + err.Error()), false
		}
	}
	var env release.Envelope
	if err := json.Unmarshal(envRaw, &env); err != nil {
		return failed(DetailSignature + ": envelope is not readable"), false
	}

	m, err := release.Verify(body, &env, cfg.Roots, now)
	if err != nil {
		return failed(reasonFor(err)), false
	}
	seen, _ := LoadSequence(cfg.StateDir)
	if err := release.Fresh(m, seen); err != nil {
		return failed(fmt.Sprintf("%s: sequence %d, already accepted %d",
			DetailStale, m.Sequence, seen)), false
	}
	// Raise the floor as soon as a manifest is accepted, not when an
	// update succeeds. The floor is about which manifests may be shown to
	// this agent again, and a download that fails afterwards does not
	// make an old manifest acceptable once more.
	if err := SaveSequence(cfg.StateDir, m.Sequence); err != nil {
		return failed(DetailCannotArm + ": recording the manifest sequence: " + err.Error()), false
	}
	art, err := m.Find(d.Version, cfg.GOOS, cfg.GOARCH)
	if err != nil {
		return failed(fmt.Sprintf("%s: %s for %s/%s is not in the manifest",
			DetailNoArtifact, d.Version, cfg.GOOS, cfg.GOARCH)), false
	}

	staged, err := fetchArtifact(ctx, cfg, d.Artifact, art)
	if err != nil {
		return failed(artifactFailure(err)), false
	}
	defer os.Remove(staged)

	// Armed before the swap, by the binary that still works. An update
	// that cannot be undone does not happen: this is the one refusal
	// that is about our own housekeeping rather than about the payload.
	if err := Arm(cfg, Probation{
		From: cfg.Current, To: d.Version, Sequence: m.Sequence,
		Deadline: now.Add(cfg.Probation),
	}); err != nil {
		return failed(DetailCannotArm + ": " + err.Error()), false
	}

	if err := swap(cfg.ExePath, staged); err != nil {
		// Leave no probation record armed for a swap that did not
		// happen, or the next start reverts a binary nobody replaced.
		_ = ClearProbation(cfg.StateDir)
		return failed(DetailSwap + ": " + err.Error()), false
	}
	return provider.Result{Status: provider.StatusApplied,
		Detail: fmt.Sprintf("%s -> %s, restarting", cfg.Current, d.Version)}, true
}

// reasonFor maps a verification error to the word the server records.
// release.Verify deliberately does not say which part of a bad signature
// was bad; this keeps that property.
func reasonFor(err error) string {
	switch {
	case errors.Is(err, release.ErrStale):
		return DetailStale + ": the manifest has expired"
	case errors.Is(err, release.ErrSignature):
		return DetailSignature + ": the manifest is not signed by a key this build trusts"
	default:
		return DetailSignature + ": " + err.Error()
	}
}

func failed(detail string) provider.Result {
	return provider.Result{Status: provider.StatusFailed, Detail: detail}
}

func fetchLimited(ctx context.Context, cfg Config, url string, max int64) ([]byte, error) {
	rc, err := cfg.Fetch(ctx, url)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("larger than %d bytes", max)
	}
	return b, nil
}

// fetchArtifact stages the file for this host. The server's copy comes
// first when the server names one. Any failure of that copy -- transport,
// status, size or hash -- falls back once to the URL in the manifest, so a
// stale or broken cache costs a download from the origin, never an update.
func fetchArtifact(ctx context.Context, cfg Config, id int, art *release.Artifact) (string, error) {
	origin := func() (io.ReadCloser, error) { return cfg.Fetch(ctx, art.URL) }
	if id <= 0 || cfg.Payload == nil {
		return stage(cfg, art, origin)
	}
	staged, serverErr := stage(cfg, art, func() (io.ReadCloser, error) {
		return payloadStream(ctx, cfg.Payload, id), nil
	})
	if serverErr == nil {
		return staged, nil
	}
	staged, originErr := stage(cfg, art, origin)
	if originErr == nil {
		return staged, nil
	}
	return "", &bothFailed{server: serverErr, origin: originErr}
}

// bothFailed is a file neither the server nor the origin could supply.
type bothFailed struct{ server, origin error }

func (b *bothFailed) Error() string {
	return "server copy: " + describe(b.server) + "; origin: " + describe(b.origin)
}

func describe(err error) string {
	if errors.Is(err, release.ErrHash) {
		return "bytes the manifest does not describe"
	}
	return err.Error()
}

// artifactFailure is the detail for a file that could not be staged, and it
// leads with the code the server classifies. hash_mismatch is kept for
// wrong bytes from every source tried, because that says something about
// what was asked for. When a source simply could not deliver, this machine
// did not get there, and that is fetch_failed.
func artifactFailure(err error) string {
	var both *bothFailed
	if errors.As(err, &both) {
		if errors.Is(both.server, release.ErrHash) && errors.Is(both.origin, release.ErrHash) {
			return DetailHash + ": the server's copy and the origin both served bytes the manifest does not describe"
		}
		return DetailFetch + ": artifact: " + both.Error()
	}
	if errors.Is(err, release.ErrHash) {
		return DetailHash + ": the bytes served are not the bytes the manifest describes"
	}
	return DetailFetch + ": artifact: " + err.Error()
}

// payloadStream turns the writer-shaped payload fetch into a reader, so the
// server's copy goes through the same CopyVerified the origin's does.
// Closing it cancels the request and waits for it to end, so a copy refused
// part way does not leave a download running under the fallback.
func payloadStream(ctx context.Context, fetch func(context.Context, int, io.Writer) error, id int) io.ReadCloser {
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		pw.CloseWithError(fetch(ctx, id, pw))
	}()
	return &pipeStream{PipeReader: pr, cancel: cancel, done: done}
}

type pipeStream struct {
	*io.PipeReader
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *pipeStream) Close() error {
	p.cancel()
	err := p.PipeReader.Close()
	<-p.done
	return err
}

// stage streams one source into the state directory, refusing bytes that
// are not the ones the manifest described. The file is written next to the
// binary it will replace, not in a temp directory, because the last step
// is a rename and a rename across filesystems is a copy.
func stage(cfg Config, art *release.Artifact, open func() (io.ReadCloser, error)) (string, error) {
	dir := filepath.Dir(cfg.ExePath)
	f, err := os.CreateTemp(dir, ".fog-agent-update-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	rc, err := open()
	if err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	err = release.CopyVerified(f, rc, art)
	rc.Close()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return "", err
	}
	if err := os.Chmod(name, 0o755); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// HTTPFetch is the Fetch a real agent uses, and exists here rather than
// in the caller so the status check cannot be forgotten by one of them.
//
// A mirror answering 404 or 503 must be reported as a fetch failure and
// nothing else. Returning the error page's body as if it were the file
// makes a broken mirror look like a bad signature, which sends an admin
// hunting for an attacker instead of a typo in a URL -- and that is the
// worst possible confusion for this particular vocabulary to have.
func HTTPFetch(c *http.Client) func(context.Context, string) (io.ReadCloser, error) {
	return func(ctx context.Context, url string) (io.ReadCloser, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 != 2 {
			resp.Body.Close()
			return nil, fmt.Errorf("%s: %s", url, resp.Status)
		}
		return resp.Body, nil
	}
}
