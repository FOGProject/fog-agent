package update

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/FOGProject/fog-agent/internal/provider"
)

const originArtifact = "/fog-agent-linux-amd64"

// counting wraps the lab's origin server so a test can say what it served.
// refuse names paths it answers 404 for, as an origin a host cannot reach.
func counting(l *lab, refuse ...string) func(path string) int {
	var mu sync.Mutex
	hits := map[string]int{}
	orig := l.srv.Config.Handler
	l.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		for _, p := range refuse {
			if r.URL.Path == p {
				http.NotFound(w, r)
				return
			}
		}
		orig.ServeHTTP(w, r)
	})
	return func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[path]
	}
}

// fogServer stands in for the FOG server's payload route.
type fogServer struct {
	srv    *httptest.Server
	status int
	body   []byte
	mu     sync.Mutex
	asked  []string
}

func newFogServer(t *testing.T, status int, body []byte) *fogServer {
	t.Helper()
	f := &fogServer{status: status, body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.asked = append(f.asked, r.URL.Path)
		f.mu.Unlock()
		w.WriteHeader(f.status)
		w.Write(f.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// payload does what enroll.Client.Payload does, against this server: the
// same route and the same refusal of anything but 200. The real client
// cannot be used here, because enroll imports this package.
func (f *fogServer) payload(ctx context.Context, id int, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/agent/v1/payload/update/%d", f.srv.URL, id), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update payload: HTTP %d", resp.StatusCode)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (f *fogServer) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// inline adds the lab's signed manifest and envelope the way a syncing
// server sends them.
func (l *lab) inline(d Desired) Desired {
	d.Manifest = base64.StdEncoding.EncodeToString(l.body)
	d.Signature = base64.StdEncoding.EncodeToString(l.env)
	return d
}

func TestRunVerifiesTheManifestTheServerSent(t *testing.T) {
	l := newLab(t)
	hits := counting(l)
	res, restart := Run(context.Background(), l.inline(Desired{Version: "0.4.2"}), l.cfg())
	if res.Status != provider.StatusApplied || !restart {
		t.Fatalf("a verified inline manifest must apply: %+v restart=%v", res, restart)
	}
	if n := hits("/manifest.json") + hits("/manifest.json.sig"); n != 0 {
		t.Errorf("the manifest URL was fetched %d times with a usable pair in hand", n)
	}
	if got := l.onDisk(l.exe); got != newBinary {
		t.Errorf("the binary was not replaced, it holds %q", got)
	}
}

// The inline pair gets no trust a download would not get. A tampered one
// is refused as it would be from a mirror, and the refusal is not quietly
// routed around by asking the URL instead.
func TestRunRefusesATamperedManifestFromTheServer(t *testing.T) {
	l := newLab(t)
	hits := counting(l)
	d := l.inline(Desired{Version: "0.4.2"})
	d.Manifest = base64.StdEncoding.EncodeToString(
		[]byte(strings.Replace(string(l.body), `"sequence":47`, `"sequence":48`, 1)))
	res, restart := Run(context.Background(), d, l.cfg())
	if res.Status != provider.StatusFailed || restart || !strings.HasPrefix(res.Detail, DetailSignature) {
		t.Fatalf("a tampered inline manifest must be refused on the signature: %+v", res)
	}
	if n := hits("/manifest.json"); n != 0 {
		t.Errorf("a refused inline manifest fell back to the URL (%d fetches)", n)
	}
	if got := l.onDisk(l.exe); got != oldBinary {
		t.Errorf("the running binary was touched: %q", got)
	}
}

func TestRunIgnoresAnIncompleteOrUnreadablePair(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Desired)
	}{
		{"a manifest with no signature", func(d *Desired) { d.Signature = "" }},
		{"a signature with no manifest", func(d *Desired) { d.Manifest = "" }},
		{"a manifest that is not base64", func(d *Desired) { d.Manifest = "not*base64!" }},
		{"a signature that is not base64", func(d *Desired) { d.Signature = "not*base64!" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)
			hits := counting(l)
			d := l.inline(Desired{Version: "0.4.2"})
			tc.edit(&d)
			res, restart := Run(context.Background(), d, l.cfg())
			if res.Status != provider.StatusApplied || !restart {
				t.Fatalf("an unusable pair must be ignored and the URL used: %+v", res)
			}
			if hits("/manifest.json") != 1 || hits("/manifest.json.sig") != 1 {
				t.Errorf("the URL was not asked: manifest %d, signature %d",
					hits("/manifest.json"), hits("/manifest.json.sig"))
			}
		})
	}
}

func TestRunTakesTheServerCopyWithoutTouchingTheOrigin(t *testing.T) {
	l := newLab(t)
	hits := counting(l)
	// An origin serving wrong bytes, so a fallback that should not have
	// happened fails the update rather than passing unnoticed.
	l.serveArtifact = []byte(strings.Repeat("x", len(newBinary)))
	f := newFogServer(t, http.StatusOK, []byte(newBinary))
	cfg := l.cfg()
	cfg.Payload = f.payload
	res, restart := Run(context.Background(), Desired{Version: "0.4.2", Artifact: 7}, cfg)
	if res.Status != provider.StatusApplied || !restart {
		t.Fatalf("a good server copy must apply: %+v", res)
	}
	if n := hits(originArtifact); n != 0 {
		t.Errorf("the origin was fetched %d times after a good server copy", n)
	}
	if got := f.calls(); len(got) != 1 || got[0] != "/agent/v1/payload/update/7" {
		t.Errorf("the server was asked %v", got)
	}
	if got := l.onDisk(l.exe); got != newBinary {
		t.Errorf("the binary was not replaced, it holds %q", got)
	}
}

func TestRunFallsBackToTheOriginWhenTheServerCopyFails(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"the server answers 500", http.StatusInternalServerError, "no"},
		{"the server serves different bytes", http.StatusOK, strings.Repeat("x", len(newBinary))},
		{"the server serves too few bytes", http.StatusOK, newBinary[:5]},
		// Past the declared size, so the copy stops reading mid-stream and
		// the abandoned download must not hang the fallback.
		{"the server serves too many bytes", http.StatusOK, strings.Repeat(newBinary, 5000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)
			hits := counting(l)
			f := newFogServer(t, tc.status, []byte(tc.body))
			cfg := l.cfg()
			cfg.Payload = f.payload
			res, restart := Run(context.Background(), Desired{Version: "0.4.2", Artifact: 7}, cfg)
			if res.Status != provider.StatusApplied || !restart {
				t.Fatalf("a failed server copy must fall back to the origin: %+v", res)
			}
			if len(f.calls()) != 1 || hits(originArtifact) != 1 {
				t.Errorf("want one server attempt and one origin fetch, got %d and %d",
					len(f.calls()), hits(originArtifact))
			}
			if got := l.onDisk(l.exe); got != newBinary {
				t.Errorf("the binary was not replaced, it holds %q", got)
			}
		})
	}
}

func TestRunNamesBothSourcesWhenNeitherDelivers(t *testing.T) {
	wrong := strings.Repeat("x", len(newBinary))
	cases := []struct {
		name         string
		serverStatus int
		serverBody   string
		originWrong  bool // origin serves wrong bytes; otherwise it answers 404
		want         string
	}{
		{"both serve wrong bytes", http.StatusOK, wrong, true, DetailHash + ":"},
		{"neither can be reached", http.StatusServiceUnavailable, "", false, DetailFetch + ":"},
		// Only one source said anything about the bytes. The other could
		// not deliver, so this is a machine that did not get there.
		{"wrong bytes from the server, no origin", http.StatusOK, wrong, false, DetailFetch + ":"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)
			if tc.originWrong {
				l.serveArtifact = []byte(wrong)
				counting(l)
			} else {
				counting(l, originArtifact)
			}
			f := newFogServer(t, tc.serverStatus, []byte(tc.serverBody))
			cfg := l.cfg()
			cfg.Payload = f.payload
			res, restart := Run(context.Background(), Desired{Version: "0.4.2", Artifact: 7}, cfg)
			if res.Status != provider.StatusFailed || restart {
				t.Fatalf("must fail: %+v", res)
			}
			if !strings.HasPrefix(res.Detail, tc.want) {
				t.Errorf("detail %q does not lead with %q", res.Detail, tc.want)
			}
			if !strings.Contains(res.Detail, "server") || !strings.Contains(res.Detail, "origin") {
				t.Errorf("detail %q does not name both attempts", res.Detail)
			}
			if got := l.onDisk(l.exe); got != oldBinary {
				t.Errorf("the running binary was touched: %q", got)
			}
		})
	}
}

// A server that sends none of the new fields gets exactly the old path,
// even on an agent that has a way to ask its server for payloads.
func TestRunWithoutTheNewFieldsTakesTheOriginalPath(t *testing.T) {
	l := newLab(t)
	hits := counting(l)
	f := newFogServer(t, http.StatusOK, []byte(newBinary))
	cfg := l.cfg()
	cfg.Payload = f.payload
	res, restart := Run(context.Background(), Desired{Version: "0.4.2"}, cfg)
	if res.Status != provider.StatusApplied || !restart {
		t.Fatalf("the original path must still apply: %+v", res)
	}
	if got := f.calls(); len(got) != 0 {
		t.Errorf("the server was asked for a payload it never offered: %v", got)
	}
	if hits("/manifest.json") != 1 || hits("/manifest.json.sig") != 1 || hits(originArtifact) != 1 {
		t.Errorf("want manifest, signature and artifact fetched once each, got %d %d %d",
			hits("/manifest.json"), hits("/manifest.json.sig"), hits(originArtifact))
	}
}
