// fog-agent is the FOG management agent. Today it reports identity, enrolls
// and polls; the providers follow the design in
// docs/design/0001-architecture.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/identity"
	"github.com/FOGProject/fog-agent/internal/provider"
	"github.com/FOGProject/fog-agent/internal/provider/hostname"
	"github.com/FOGProject/fog-agent/internal/reboot"
)

// Version is stamped by the release build (-ldflags "-X main.Version=...").
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "identity":
		err = printJSON(identity.Read())
	case "version":
		fmt.Printf("fog-agent %s %s/%s\n", Version, runtime.GOOS, runtime.GOARCH)
	case "enroll":
		err = cmdEnroll(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "renew":
		err = cmdRenew(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fog-agent:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  fog-agent identity                      print the SMBIOS identity and MACs
  fog-agent enroll --server URL --ca FILE [--token T] [--once] [--dir DIR]
  fog-agent run [--server URL] [--ca FILE] [--token T] [--once] [--dir DIR]
                                          enroll if needed, then poll the server
  fog-agent renew [--dir DIR]             renew the certificate now, whatever its expiry
  fog-agent status [--dir DIR]
  fog-agent version`)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// sayer prints a line only when it changes, so a loop that repeats the
// same state every few minutes writes one line per change of state, not
// one per iteration (design doc 5.1: informative, never noisy).
type sayer struct{ last string }

func (s *sayer) say(msg string) {
	if msg != s.last {
		fmt.Fprintln(os.Stderr, msg)
		s.last = msg
	}
}

// commonFlags are shared by enroll and run.
type commonFlags struct {
	server, caPath, token, dir *string
}

func addCommonFlags(fs *flag.FlagSet) commonFlags {
	return commonFlags{
		server: fs.String("server", "", "FOG server base URL, e.g. https://fog.example.org/fog (remembered after the first enroll)"),
		caPath: fs.String("ca", "", "PEM bundle to trust for the server (the FOG CA, or the public CA the web UI uses; remembered)"),
		token:  fs.String("token", "", "enrollment token minted by an admin (optional)"),
		dir:    fs.String("dir", enroll.DefaultDir, "state directory"),
	}
}

// openState loads the state directory and settles the server URL and CA
// bundle from flags or from what an earlier enroll remembered.
func openState(f commonFlags) (*enroll.State, []byte, error) {
	st, err := enroll.Load(*f.dir)
	if err != nil {
		return nil, nil, err
	}
	if *f.server != "" {
		st.Config.ServerURL = *f.server
	}
	if st.Config.ServerURL == "" {
		return nil, nil, errors.New("--server is required the first time")
	}
	caPEM := st.CA()
	if *f.caPath != "" {
		if caPEM, err = os.ReadFile(*f.caPath); err != nil {
			return nil, nil, err
		}
		if err := st.SaveCA(caPEM); err != nil {
			return nil, nil, err
		}
	}
	if len(caPEM) == 0 {
		return nil, nil, errors.New("--ca is required the first time: the agent trusts only the bundle it is given")
	}
	if err := st.SaveConfig(); err != nil {
		return nil, nil, err
	}
	return st, caPEM, nil
}

// enrollRequest reads the machine's identity, settles the key for it and
// builds the request protocol-v1.md describes.
func enrollRequest(st *enroll.State, token string, out *sayer) (enroll.Request, error) {
	live := identity.Read()
	regen, err := st.EnsureKey(live)
	if err != nil {
		return enroll.Request{}, err
	}
	if regen {
		out.say("generated a new key for this machine's identity")
	}
	csr, err := st.CSR()
	if err != nil {
		return enroll.Request{}, err
	}
	hostname, _ := os.Hostname()
	return enroll.Request{
		Protocol: enroll.Protocol, AgentVersion: Version,
		OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: hostname,
		Identity: live, CSRPEM: string(csr), Token: token,
	}, nil
}

// enrollLoop repeats the same request until the server issues, and keeps
// repeating on a slower cadence when denied so a later approval is still
// reachable. With once set it returns after a single exchange, issued or
// not, and the caller prints the answer.
func enrollLoop(ctx context.Context, st *enroll.State, client *enroll.Client, req enroll.Request, once bool, out *sayer) (*enroll.Response, error) {
	for {
		resp, err := client.Enroll(ctx, req)
		wait := 5 * time.Minute
		switch {
		case err != nil:
			out.say("enroll: " + err.Error())
		case resp.Status == enroll.StatusIssued:
			if err := st.SaveIssued([]byte(resp.CertificatePEM), resp.HostID); err != nil {
				return nil, err
			}
			out.say(fmt.Sprintf("enrolled as host %d, certificate valid until %s", resp.HostID, resp.NotAfter))
			return resp, nil
		case resp.Status == enroll.StatusPending:
			out.say(fmt.Sprintf("pending approval on the server (%s); waiting", resp.Reason))
			if resp.RetryAfter > 0 {
				wait = time.Duration(resp.RetryAfter) * time.Second
			}
		case resp.Status == enroll.StatusDenied:
			out.say(fmt.Sprintf("denied by the server (%s); retrying hourly", resp.Reason))
			wait = time.Hour
		default:
			out.say("server does not support agent protocol 1; retrying hourly")
			wait = time.Hour
		}
		if once {
			return resp, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

func cmdEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	f := addCommonFlags(fs)
	once := fs.Bool("once", false, "send one request and exit instead of waiting for approval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, caPEM, err := openState(f)
	if err != nil {
		return err
	}
	out := &sayer{}
	req, err := enrollRequest(st, *f.token, out)
	if err != nil {
		return err
	}
	client, err := enroll.NewClient(st.Config.ServerURL, caPEM)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	resp, err := enrollLoop(ctx, st, client, req, *once, out)
	if err != nil {
		return err
	}
	if *once {
		return printJSON(resp)
	}
	return nil
}

// cmdRun is the service loop: enroll if there is no certificate, then poll
// with it. A 401 from the poll means the server no longer knows this
// certificate -- the host was deleted, or re-enrolled from elsewhere -- so
// the agent drops it and goes back to enrolling, where it will be pended
// for an admin like any unknown machine.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	f := addCommonFlags(fs)
	once := fs.Bool("once", false, "one poll (enrolling first if needed), print the answer and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, caPEM, err := openState(f)
	if err != nil {
		return err
	}
	client, err := enroll.NewClient(st.Config.ServerURL, caPEM)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	out := &sayer{}
	for {
		if len(st.Cert) == 0 {
			req, err := enrollRequest(st, *f.token, out)
			if err != nil {
				return err
			}
			resp, err := enrollLoop(ctx, st, client, req, *once, out)
			if err != nil {
				return err
			}
			if *once && resp.Status != enroll.StatusIssued {
				return printJSON(resp)
			}
		}
		if err := client.UseCertificate(st.Cert, st.Key); err != nil {
			return err
		}
		wait := 5 * time.Minute
		resp, err := client.Poll(ctx, enroll.PollRequest{AgentVersion: Version})
		switch {
		case errors.Is(err, enroll.ErrUnauthorized):
			out.say("server no longer recognizes this certificate; enrolling again")
			if err := st.DropIssued(); err != nil {
				return err
			}
			if *once {
				return err
			}
			continue
		case err != nil:
			out.say("poll: " + err.Error())
		default:
			out.say(fmt.Sprintf("host %d (%s), server capabilities: [%s]", resp.Host.ID, resp.Host.Name, strings.Join(resp.Capabilities, " ")))
			if resp.PollInterval > 0 {
				wait = time.Duration(resp.PollInterval) * time.Second
			}
			// Renewal rides the same session, once the poll has proved
			// it. A failed renewal is reported and retried next poll;
			// the current certificate keeps working until it expires.
			if enroll.RenewDue(st.Cert, time.Now()) {
				if err := renew(ctx, st, client, out); err != nil {
					out.say("renew: " + err.Error())
				}
			}
			// Converge when the server's revision is not the one this
			// agent last applied. A failure leaves the revision
			// unapplied, so the next poll tries again.
			if resp.StateRevision != st.Config.AppliedRevision {
				if err := reconcile(ctx, st, client, out); err != nil {
					out.say("reconcile: " + err.Error())
				}
			}
			// The coordinator runs every poll, not only when state
			// moved: a reboot deferred for a logged-in user becomes
			// due when they leave, and nothing else notices that.
			if err := coordinate(ctx, st, client, out); err != nil {
				out.say("reboot: " + err.Error())
			}
			if *once {
				return printJSON(resp)
			}
		}
		if *once {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// renew asks for a new certificate for the current key and switches the
// client to it. The old certificate is not touched until the new one is
// on disk, so a failure anywhere leaves the agent as it was.
func renew(ctx context.Context, st *enroll.State, client *enroll.Client, out *sayer) error {
	csr, err := st.CSR()
	if err != nil {
		return err
	}
	resp, err := client.Renew(ctx, csr)
	if err != nil {
		return err
	}
	if err := st.SaveIssued([]byte(resp.CertificatePEM), resp.HostID); err != nil {
		return err
	}
	if err := client.UseCertificate(st.Cert, st.Key); err != nil {
		return err
	}
	out.say(fmt.Sprintf("certificate renewed, valid until %s", resp.NotAfter))
	return nil
}

// reconcile fetches the desired state and runs every provider the server
// listed, reporting each result. The revision is recorded as applied only
// when nothing failed: a failed provider is retried on the next poll
// rather than forgotten.
func reconcile(ctx context.Context, st *enroll.State, client *enroll.Client, out *sayer) error {
	desired, err := client.State(ctx)
	if err != nil {
		return err
	}
	if desired.Reboot != nil {
		st.Config.RebootGrace = desired.Reboot.Grace
	}
	allOK := true
	for _, capability := range desired.Capabilities {
		var r provider.Result
		force := false
		switch capability {
		case "hostname":
			if desired.Hostname == nil {
				continue
			}
			r = hostname.Ensure(*desired.Hostname)
			force = desired.Hostname.Enforce
		case "taskreboot":
			// Not a provider: a waiting task is a reboot request, and
			// a cancelled one withdraws it. The coordinator acts on it
			// below, under the same policy as everything else.
			if desired.Task == nil || desired.Task.ID == st.Config.RebootedForTask {
				st.Config.PendingReboot = reboot.Drop(st.Config.PendingReboot, reboot.SourceTask)
				continue
			}
			st.Config.PendingReboot = reboot.Merge(st.Config.PendingReboot, reboot.Reason{
				Source: reboot.SourceTask,
				Detail: fmt.Sprintf("task %d (%s)", desired.Task.ID, desired.Task.Type),
				Force:  desired.Task.Force,
				TaskID: desired.Task.ID,
			})
			out.say(fmt.Sprintf("%s: task %d (%s) waiting", capability, desired.Task.ID, desired.Task.Type))
			continue
		default:
			// A capability this build has no provider for: the server
			// is newer, and that is fine (design 0001 5.1).
			continue
		}
		out.say(fmt.Sprintf("%s: %s (%s)", capability, r.Status, r.Detail))
		if err := client.Result(ctx, enroll.ResultRequest{
			Revision: desired.Revision, Capability: capability, Status: r.Status, Detail: r.Detail,
		}); err != nil {
			out.say("result: " + err.Error())
		}
		switch r.Status {
		case provider.StatusFailed:
			allOK = false
		case provider.StatusPendingReboot:
			st.Config.PendingReboot = reboot.Merge(st.Config.PendingReboot, reboot.Reason{
				Source: capability, Detail: r.Detail, Force: force,
			})
		}
	}
	if !allOK {
		// Keep whatever reasons were recorded; only the revision waits.
		_ = st.SaveConfig()
		return errors.New("a provider failed; will retry next poll")
	}
	st.Config.AppliedRevision = desired.Revision
	return st.SaveConfig()
}

// coordinate is the reboot coordinator's turn: with reasons outstanding it
// looks at who is logged in, applies the policy, reports the decision as
// a result, and reboots when allowed. It is the only caller of
// reboot.Execute in the agent.
func coordinate(ctx context.Context, st *enroll.State, client *enroll.Client, out *sayer) error {
	if len(st.Config.PendingReboot) == 0 {
		return nil
	}
	users, err := reboot.LoggedIn()
	if err != nil {
		return err
	}
	d := reboot.Decide(st.Config.PendingReboot, users, reboot.Policy{Grace: st.Config.RebootGrace})
	status := provider.StatusPendingReboot
	if d.Reboot {
		status = provider.StatusApplied
	}
	out.say(fmt.Sprintf("reboot: %s (%s)", status, d.Why))
	if err := client.Result(ctx, enroll.ResultRequest{
		Revision: st.Config.AppliedRevision, Capability: "reboot", Status: status, Detail: d.Why,
	}); err != nil {
		out.say("result: " + err.Error())
	}
	if !d.Reboot {
		return nil
	}
	// Persist before asking: the reboot is asynchronous and the reasons
	// are satisfied the moment it is accepted. A refused request puts
	// them back.
	reasons := st.Config.PendingReboot
	for _, r := range reasons {
		if r.TaskID != 0 {
			st.Config.RebootedForTask = r.TaskID
		}
	}
	st.Config.PendingReboot = nil
	if err := st.SaveConfig(); err != nil {
		return err
	}
	if err := reboot.Execute(d.Delay, "FOG: rebooting for "+d.Why); err != nil {
		st.Config.PendingReboot = reasons
		st.Config.RebootedForTask = 0
		_ = st.SaveConfig()
		return err
	}
	return nil
}

// cmdRenew renews now, regardless of expiry: an operator's rotation, and
// the way a lab proves the route without waiting out the lead time.
func cmdRenew(args []string) error {
	fs := flag.NewFlagSet("renew", flag.ContinueOnError)
	dir := fs.String("dir", enroll.DefaultDir, "state directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := enroll.Load(*dir)
	if err != nil {
		return err
	}
	if len(st.Cert) == 0 {
		return errors.New("not enrolled: nothing to renew")
	}
	if st.Config.ServerURL == "" || len(st.CA()) == 0 {
		return errors.New("no server or CA remembered; enroll first")
	}
	client, err := enroll.NewClient(st.Config.ServerURL, st.CA())
	if err != nil {
		return err
	}
	if err := client.UseCertificate(st.Cert, st.Key); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	out := &sayer{}
	if err := renew(ctx, st, client, out); err != nil {
		return err
	}
	return printJSON(map[string]any{"host_id": st.Config.HostID, "enrolled": true})
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	dir := fs.String("dir", enroll.DefaultDir, "state directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := enroll.Load(*dir)
	if err != nil {
		return err
	}
	out := map[string]any{
		"state_dir": st.Dir,
		"server":    st.Config.ServerURL,
		"host_id":   st.Config.HostID,
		"has_key":   st.Key != nil,
		"enrolled":  len(st.Cert) > 0,
	}
	if st.Identity != nil {
		out["key_identity"] = st.Identity.Identity
	}
	return printJSON(out)
}
