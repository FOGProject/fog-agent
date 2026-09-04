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
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/identity"
	"github.com/FOGProject/fog-agent/internal/printers"
	"github.com/FOGProject/fog-agent/internal/provider"
	"github.com/FOGProject/fog-agent/internal/provider/hostname"
	"github.com/FOGProject/fog-agent/internal/provider/power"
	"github.com/FOGProject/fog-agent/internal/provider/printerset"
	"github.com/FOGProject/fog-agent/internal/provider/snapin"
	"github.com/FOGProject/fog-agent/internal/provider/software"
	"github.com/FOGProject/fog-agent/internal/reboot"
)

// Version is stamped by the release build (-ldflags "-X main.Version=...").
var Version = "dev"

func main() {
	// Under the Windows service control manager there is no terminal:
	// the same run loop, with the log in a file and a stop request
	// canceling the context (service_windows.go).
	if isService() {
		if err := runService(); err != nil {
			os.Exit(1)
		}
		return
	}
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
	case "setup":
		err = cmdSetup(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "renew":
		err = cmdRenew(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "service":
		err = cmdService(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(logOut, "fog-agent:", err)
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
  fog-agent service install --server URL --ca FILE [--token T] [--dir DIR]
  fog-agent setup --server URL --ca FILE [--token T] [--dir DIR]
                                          Windows: install and start the service
  fog-agent service uninstall|start|stop|status
  fog-agent version`)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// logOut is where the agent talks: stderr from a terminal, the log file
// under the service (runService swaps it before anything is said).
var logOut io.Writer = os.Stderr

// sayer prints a line only when it changes, so a loop that repeats the
// same state every few minutes writes one line per change of state, not
// one per iteration (design doc 5.1: informative, never noisy). Under
// the service each line is stamped, since nothing else dates it.
type sayer struct{ last string }

func (s *sayer) say(msg string) {
	if msg != s.last {
		if logOut != os.Stderr {
			msg = time.Now().Format(time.RFC3339) + " " + msg
		}
		fmt.Fprintln(logOut, msg)
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
			// The token, if one was kept for this attempt, is spent.
			st.Config.PendingToken = ""
			if err := st.SaveIssued([]byte(resp.CertificatePEM), resp.HostID); err != nil {
				return nil, err
			}
			if err := st.SaveConfig(); err != nil {
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runAgent(ctx, args)
}

// runAgent is the agent: enroll if needed, then poll until ctx ends. The
// command line gives it an interrupt context; the Windows service gives it
// one the service control manager cancels.
func runAgent(ctx context.Context, args []string) error {
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
	out := &sayer{}
	watch := &sessionWatcher{}
	for {
		if len(st.Cert) == 0 {
			// A token setup kept for us outranks nothing on the command
			// line: the service is registered with plain `run`.
			token := *f.token
			if token == "" {
				token = st.Config.PendingToken
			}
			req, err := enrollRequest(st, token, out)
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
		// The poll carries what this agent applied; the server answers
		// with the state only when that is not current. A build that
		// learned a capability sends nothing, so it gets the state once.
		req := enroll.PollRequest{AgentVersion: Version, AppliedRevision: st.Config.AppliedRevision, WantState: driftMayBeDue(st)}
		if st.Config.AppliedWith != supportedCapabilities {
			req.AppliedRevision = ""
		}
		// Facts ride up the same request the desired state comes down
		// (design 0006): sent only when their hash moved or the server
		// asked, and recorded only once the poll has succeeded.
		now := time.Now()
		sent := attachFacts(st, &req, now, out)
		sessionDigest, sessionsSent := watch.attach(st, &req, now)
		resp, err := client.Poll(ctx, req)
		switch {
		case errors.Is(err, enroll.ErrCertificateUnknown):
			// The server has this certificate and says it binds to no
			// host. That is the one 401 worth acting on.
			out.say("server no longer recognizes this certificate; enrolling again")
			if err := st.DropIssued(); err != nil {
				return err
			}
			if *once {
				return err
			}
			continue
		case errors.Is(err, enroll.ErrUnauthorized):
			// A 401 that is NOT about this binding: no certificate reached
			// the application, the database was down, a proxy answered, or
			// the webroot does not serve the agent routes. The certificate
			// is probably fine and re-enrolling would need an admin to
			// approve this host again, so keep it and keep asking.
			drop, err := unauthorizedTooLong(st, now)
			if err != nil {
				return err
			}
			if !drop {
				out.say("server refused this certificate (" + unauthorizedFor(st, now) +
					"); keeping it and retrying")
				if *once {
					return enroll.ErrUnauthorized
				}
				// NOT continue. This branch changes nothing -- the
				// certificate is kept and the next poll is byte for byte
				// the same request -- so skipping the wait at the bottom
				// of the loop retries the identical failing call as fast
				// as the network answers. Observed on telliottwin11 over a
				// six-minute window while the webroot was mid-deploy:
				// 15,100 identical lines, 1.37 MB of log, every one of
				// them stamped the same second.
				//
				// break leaves the switch, not the loop, so control
				// reaches waitOrFire like any other poll outcome. Falling
				// out of this if would run the drop path below instead.
				break
			}
			out.say("server has refused this certificate for " + unauthorizedFor(st, now) +
				"; enrolling again")
			if err := st.DropIssued(); err != nil {
				return err
			}
			if *once {
				return enroll.ErrUnauthorized
			}
			continue
		case err != nil:
			out.say("poll: " + err.Error())
		default:
			// A poll got through, so any run of refusals is over: it must
			// not carry over toward the grace period of a later, unrelated
			// outage.
			if err := clearUnauthorized(st); err != nil {
				out.say("state: " + err.Error())
			}
			if resp.State != nil {
				out.say(fmt.Sprintf("host %d (%s), server capabilities: [%s]", resp.Host.ID, resp.Host.Name, strings.Join(resp.State.Capabilities, " ")))
			}
			if resp.PollInterval > 0 {
				wait = time.Duration(resp.PollInterval) * time.Second
			}
			if err := recordSessions(st, sessionDigest, sessionsSent, resp, now); err != nil {
				out.say("state: " + err.Error())
			}
			if err := recordFacts(st, sent, resp, now); err != nil {
				out.say("facts: " + err.Error())
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
			switch {
			case needsReconcile(st.Config, resp.Revision) && resp.State == nil:
				out.say("poll: the revision moved but the server sent no state")
			case needsReconcile(st.Config, resp.Revision):
				if err := reconcile(ctx, st, client, resp.State, out); err != nil {
					out.say("reconcile: " + err.Error())
				}
			case resp.State != nil && driftMayBeDue(st):
				// The drift check: the set has not changed, the host
				// might have. The interval is the one the last
				// reconcile saw, so this costs nothing until it is due.
				if err := drift(ctx, st, client, resp.State, out); err != nil {
					out.say("software drift: " + err.Error())
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
		// Sleep until the poll is due or a power schedule fires,
		// whichever is first; a firing runs the coordinator right away.
		if fired, err := waitOrFire(ctx, st, client, wait, watch, out); err != nil {
			return err
		} else if fired {
			continue
		}
	}
}

// waitOrFire sleeps for wait unless a power schedule is due sooner, in
// which case it records the firing, hands the coordinator a forced
// reason and runs it, and reports true so the loop polls again at once
// (a shutdown that went through never returns here).
func waitOrFire(ctx context.Context, st *enroll.State, client *enroll.Client, wait time.Duration, watch *sessionWatcher, out *sayer) (bool, error) {
	now := time.Now()
	after := now
	if st.Config.PowerFired.After(after) {
		after = st.Config.PowerFired
	}
	next, sched, ok := power.Next(st.Config.PowerSchedules, after)
	if !ok || next.Sub(now) >= wait {
		if err := sleepSampling(ctx, st, watch, wait, out); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := sleepSampling(ctx, st, watch, next.Sub(now), out); err != nil {
		return false, err
	}
	st.Config.PowerFired = next
	st.Config.PendingReboot = reboot.Merge(st.Config.PendingReboot, reboot.Reason{
		Source: "power", Detail: fmt.Sprintf("scheduled %s (%s)", sched.Action, sched.Cron), Force: true, Mode: sched.Action,
	})
	if err := st.SaveConfig(); err != nil {
		return false, err
	}
	out.say(fmt.Sprintf("power: scheduled %s (%s) is due", sched.Action, sched.Cron))
	if err := coordinate(ctx, st, client, out); err != nil {
		out.say("reboot: " + err.Error())
	}
	return true, nil
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

// reconcile runs every provider the server listed in the desired state,
// reporting each result. The revision is recorded as applied only
// when nothing failed: a failed provider is retried on the next poll
// rather than forgotten.
func reconcile(ctx context.Context, st *enroll.State, client *enroll.Client, desired *enroll.DesiredState, out *sayer) error {
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
		case "power":
			// Schedules are kept for the run loop to fire between polls;
			// an on-demand action is a forced request to the coordinator,
			// accepted here and consumed on the server by that report.
			if desired.Power == nil {
				st.Config.PowerSchedules = nil
				continue
			}
			st.Config.PowerSchedules = desired.Power.Schedules
			for _, od := range desired.Power.OnDemand {
				st.Config.PendingReboot = reboot.Merge(st.Config.PendingReboot, reboot.Reason{
					Source: "power", Detail: fmt.Sprintf("on-demand %s", od.Action), Force: true, Mode: od.Action,
				})
				out.say(fmt.Sprintf("power: on-demand %s accepted", od.Action))
				if _, err := client.Result(ctx, enroll.ResultRequest{
					Revision: desired.Revision, Capability: capability, Status: provider.StatusApplied,
					Detail: fmt.Sprintf("on-demand %s accepted (id %d)", od.Action, od.ID),
				}); err != nil {
					out.say("result: " + err.Error())
					allOK = false
				}
			}
			continue
		case "software":
			// The desired package set, converged in order. Outcomes do
			// not hold the revision: a failed install is reported and
			// tried again at the drift check, not every poll. Only a
			// report that did not land keeps the revision unapplied.
			if desired.Software == nil {
				st.Config.SoftwareDrift = 0
				continue
			}
			st.Config.SoftwareDrift = desired.Software.DriftInterval
			if !runSoftware(ctx, st, client, desired.Revision, desired.Software, out) {
				allOK = false
			}
			continue
		case "printers":
			// The assigned print queues, converged and reported one by
			// one. Like software and unlike a snapin, nothing here is a
			// task: the set is read fresh every time and an outcome
			// refreshes a row rather than closing one.
			if desired.Printers == nil {
				st.Config.PrintersManaged = false
				continue
			}
			st.Config.PrintersManaged = desired.Printers.Manage != printerset.ManageOff && desired.Printers.Manage != ""
			if !runPrinters(ctx, st, client, desired.Revision, desired.Printers, out) {
				allOK = false
			}
			continue
		case "snapin":
			// The queue in the server's run order, one at a time. A
			// task that could not be fetched stays open for the next
			// poll; everything else closes with a result. The reboot
			// or shutdown a snapin asks for is a reason for the
			// coordinator, forced the way the legacy client's was.
			if !runSnapins(ctx, st, client, desired.Snapins, out) {
				allOK = false
			}
			continue
		default:
			// A capability this build has no provider for: the server
			// is newer, and that is fine (design 0001 5.1).
			continue
		}
		out.say(fmt.Sprintf("%s: %s (%s)", capability, r.Status, r.Detail))
		if _, err := client.Result(ctx, enroll.ResultRequest{
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
	st.Config.AppliedWith = supportedCapabilities
	return st.SaveConfig()
}

// supportedCapabilities names what this build's reconcile handles, in the
// order of its switch. It is stored next to the applied revision so an
// upgrade that learns a capability re-runs the revision it inherited:
// the Windows lab upgrade to the power build sat on an on-demand shutdown
// for ten minutes because the old binary had already marked that revision
// applied, and nothing moved it until an unrelated task did.
const supportedCapabilities = "hostname,taskreboot,power,software,printers,snapin"

// needsReconcile says whether the server's revision must be converged: it
// is not the one applied, or it was applied by a build that handled a
// different set of capabilities.
func needsReconcile(cfg enroll.Config, revision string) bool {
	return revision != cfg.AppliedRevision || cfg.AppliedWith != supportedCapabilities
}

// runSnapins runs the queue in order and reports each task. It returns
// false when a task is left open (a fetch failed) or a report did not
// land, so the revision stays unapplied and the next poll comes back.
func runSnapins(ctx context.Context, st *enroll.State, client *enroll.Client, queue []snapin.Task, out *sayer) bool {
	dir := filepath.Join(st.Dir, "snapins")
	for _, t := range queue {
		r := snapin.Run(ctx, t, dir, func(ctx context.Context, w io.Writer) error {
			return client.Payload(ctx, "snapin", t.ID, w)
		})
		if !r.Fetched {
			out.say(fmt.Sprintf("snapin %q (task %d): %s; will retry next poll", t.Name, t.ID, r.Details))
			return false
		}
		outcome, err := client.Result(ctx, enroll.ResultRequest{
			Revision: st.Config.AppliedRevision, Capability: "snapin", Status: provider.StatusApplied,
			Item: &enroll.ResultItem{ID: t.ID, Status: r.Status, ExitCode: r.ExitCode, Details: r.Details},
		})
		if err != nil {
			out.say("snapin result: " + err.Error())
			return false
		}
		out.say(fmt.Sprintf("snapin %q (task %d): %s, exit %d, outcome %s (%s)", t.Name, t.ID, r.Status, r.ExitCode, outcome, firstLine(r.Details)))
		// The server read the exit code against the snapin's return-code
		// table; the agent acts on its answer, not on the number.
		switch outcome {
		case enroll.OutcomeRetry:
			// The task went back to queued (an installer was busy, say).
			// Stop here so the revision stays unapplied and the next poll
			// starts the queue again from this task.
			out.say("snapin: retry later; stopping the queue for this poll")
			return false
		case enroll.OutcomeReboot:
			st.Config.PendingReboot = reboot.Merge(st.Config.PendingReboot, reboot.Reason{
				Source: "snapin", Detail: fmt.Sprintf("snapin %q asked for a reboot (exit %d)", t.Name, r.ExitCode), Force: true, Mode: reboot.ModeReboot,
			})
		case enroll.OutcomeFailed:
			if t.AbortOnFail {
				out.say("snapin: job aborts on failure; leaving the rest to the server")
				return true
			}
		}
		if t.Action != "" {
			st.Config.PendingReboot = reboot.Merge(st.Config.PendingReboot, reboot.Reason{
				Source: "snapin", Detail: fmt.Sprintf("snapin %q", t.Name), Force: true, Mode: t.Action,
			})
		}
	}
	return true
}

// runSoftware converges the software set and reports every entry. It
// returns false only when a report did not land.
func runSoftware(ctx context.Context, st *enroll.State, client *enroll.Client, revision string, policy *software.Policy, out *sayer) bool {
	backend := &software.Choco{}
	bootstrapFailed := false
	if ok, _ := backend.Available(); !ok && policy.Bootstrap.URL != "" {
		// The backend is missing and the server says to install it.
		// Reported as a result on the capability, so the audit says
		// where Chocolatey came from; the entries then run as usual.
		out.say("software: Chocolatey is missing; bootstrapping from " + policy.Bootstrap.URL)
		bctx, cancel := context.WithTimeout(ctx, software.BootstrapTimeout)
		tail, err := software.InstallChoco(bctx, software.BootstrapClient(st.CA()), policy.Bootstrap, st.Dir)
		cancel()
		status, detail := provider.StatusApplied, "Chocolatey bootstrap: installed from "+policy.Bootstrap.URL
		if err != nil {
			bootstrapFailed = true
			status, detail = provider.StatusFailed, "Chocolatey bootstrap failed: "+err.Error()+"\n"+tail
		}
		out.say(firstLine(detail))
		if _, err := client.Result(ctx, enroll.ResultRequest{Revision: revision, Capability: "software", Status: status, Detail: detail}); err != nil {
			out.say("result: " + err.Error())
		}
	}
	reports := software.Converge(ctx, backend, policy.Entries)
	ok := true
	for _, r := range reports {
		outcome, err := client.Result(ctx, enroll.ResultRequest{
			Revision: revision, Capability: "software", Status: provider.StatusApplied,
			Item: &enroll.ResultItem{ID: r.Entry.ID, Status: r.Status, ExitCode: r.ExitCode, InstalledVersion: r.InstalledVersion, Details: r.Details},
		})
		if err != nil {
			out.say(fmt.Sprintf("software %q: %s", r.Entry.Package, err))
			ok = false
			continue
		}
		out.say(fmt.Sprintf("software %q: %s, exit %d, outcome %s, installed %q (%s)", r.Entry.Package, r.Status, r.ExitCode, outcome, r.InstalledVersion, firstLine(r.Details)))
		if outcome == enroll.OutcomeReboot {
			// Not forced: an installer that wants a reboot to finish can
			// wait for the logged-in user, unlike a task or a snapin.
			st.Config.PendingReboot = reboot.Merge(st.Config.PendingReboot, reboot.Reason{
				Source: "software", Detail: fmt.Sprintf("%s asked for a reboot (exit %d)", r.Entry.Package, r.ExitCode), Mode: reboot.ModeReboot,
			})
		}
	}
	st.Config.SoftwareChecked = time.Now()
	// A set the backend could not run at all is reported once, and the
	// binary is then looked for every poll: installing Chocolatey is the
	// admin's next move, and six hours is the wrong wait for it.
	st.Config.SoftwareBlocked = len(reports) > 0
	for _, r := range reports {
		if r.Status != software.StatusCannotRun {
			st.Config.SoftwareBlocked = false
		}
	}
	if bootstrapFailed {
		// Not every poll: a bootstrap that failed is tried again at the
		// drift check, like any other failed action.
		st.Config.SoftwareBlocked = false
	}
	return ok
}

// runPrinters converges the assigned print queues and reports every one of
// them, including the ones that needed nothing. It returns false only when a
// report did not land.
//
// The two ways nothing can be attempted are reported rather than swallowed:
// no lpadmin, and an installed set that could not be read. The second is the
// one worth spelling out -- converging against an unknown installed set would
// decide "not installed" for every printer and run lpadmin against the whole
// estate every poll, which is the hot loop this design keeps avoiding.
func runPrinters(ctx context.Context, st *enroll.State, client *enroll.Client, revision string, policy *printerset.Policy, out *sayer) bool {
	if !st.Config.PrintersManaged {
		return true
	}
	backend := printerset.Native()
	var reports []printerset.Report
	switch observed, read := printers.Gather(); {
	case !read:
		reports = printerset.Unsupported(*policy, "the installed printers could not be read; nothing was changed")
	default:
		if ok, why := backend.Available(); !ok {
			reports = printerset.Unsupported(*policy, why)
			break
		}
		reports = printerset.Converge(ctx, backend, *policy, observed)
	}

	ok := true
	for _, r := range reports {
		outcome, err := client.Result(ctx, enroll.ResultRequest{
			Revision: revision, Capability: "printers", Status: provider.StatusApplied,
			Item: &enroll.ResultItem{ID: r.Printer.ID, Status: r.Status, Details: r.Error},
		})
		if err != nil {
			out.say(fmt.Sprintf("printer %q: %s", r.Printer.Name, err))
			ok = false
			continue
		}
		// The provider's own words when there are any: a printer that
		// would not install is the whole reason this capability exists,
		// and the reason it did not is the useful half of the line.
		why := r.Detail
		if r.Error != "" {
			why = r.Error
		}
		out.say(fmt.Sprintf("printer %q: %s, outcome %s (%s)", r.Printer.Name, r.Status, outcome, firstLine(why)))
	}
	st.Config.PrintersChecked = time.Now()
	return ok
}

// printersDriftDue says whether the assigned printers should be re-converged
// without the revision having moved. FactsInterval, because that is the
// cadence at which the agent looks at the printer set anyway.
func printersDriftDue(cfg enroll.Config, now time.Time) bool {
	return cfg.PrintersManaged && now.Sub(cfg.PrintersChecked) >= FactsInterval
}

// blockedCleared says whether a backend that was missing at the last run
// is there now; a stat, so it costs nothing per poll.
func blockedCleared(st *enroll.State) bool {
	if !st.Config.SoftwareBlocked {
		return false
	}
	ok, _ := (&software.Choco{}).Available()
	return ok
}

// driftDue says whether the software set should be re-checked without
// the revision having moved.
func driftDue(st *enroll.State, policy *software.Policy, now time.Time) bool {
	if policy == nil || len(policy.Entries) == 0 || policy.DriftInterval <= 0 {
		return false
	}
	return now.Sub(st.Config.SoftwareChecked) >= time.Duration(policy.DriftInterval)*time.Second || blockedCleared(st)
}

// firstLine is enough of a detail for the console; the server has the rest.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// driftMayBeDue is the cheap half of the drift decision, made before the
// poll so the state can be asked for: the interval the last reconcile
// saw has passed, a blocked set may have become runnable, or the printers
// are due another look.
func driftMayBeDue(st *enroll.State) bool {
	return softwareDriftMayBeDue(st) || printersDriftDue(st.Config, time.Now())
}

// softwareDriftMayBeDue is that decision for the software set alone.
func softwareDriftMayBeDue(st *enroll.State) bool {
	return st.Config.SoftwareDrift > 0 && (time.Since(st.Config.SoftwareChecked) >= time.Duration(st.Config.SoftwareDrift)*time.Second || blockedCleared(st))
}

// drift converges the parts of a state whose revision has not moved,
// leaving the applied revision alone. Each subsystem decides for itself
// whether it is due; the poll asked for the state because at least one was.
func drift(ctx context.Context, st *enroll.State, client *enroll.Client, desired *enroll.DesiredState, out *sayer) error {
	if driftDue(st, desired.Software, time.Now()) {
		runSoftware(ctx, st, client, desired.Revision, desired.Software, out)
	}
	if desired.Printers != nil && printersDriftDue(st.Config, time.Now()) {
		runPrinters(ctx, st, client, desired.Revision, desired.Printers, out)
	}
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
	out.say(fmt.Sprintf("reboot: %s (%s%s)", status, d.Why, map[bool]string{true: ", mode " + d.Mode, false: ""}[d.Reboot]))
	if _, err := client.Result(ctx, enroll.ResultRequest{
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
	if err := reboot.Execute(d.Mode, d.Delay, "FOG: "+d.Mode+" for "+d.Why); err != nil {
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

// cmdSetup prepares the state directory for a service that something else
// registers and starts: the MSI on Windows, a package's unit elsewhere. It
// is the install-time half of `service install` without the service
// control manager half.
func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	f := addCommonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Under msiexec there is no console, so what setup says goes to the
	// service log as well, where the person reading an install failure
	// will look.
	if lf, err := setupLog(*f.dir); err == nil && lf != nil {
		defer lf.Close()
		logOut = io.MultiWriter(logOut, lf)
	}
	out := &sayer{}
	if _, _, err := prepareState(f, out); err != nil {
		return err
	}
	return postSetup()
}

// prepareState settles the state directory: server and CA remembered, the
// directory locked down before a key is written into it, and one
// enrollment attempt. A server that does not answer is not an error here.
// The token is kept for the service's first attempt instead, so an install
// run by a deployment tool does not fail on a network blip and does not
// lose the token. A fresh directory with no server or CA is an error.
func prepareState(f commonFlags, out *sayer) (*enroll.State, []byte, error) {
	st, caPEM, err := openState(f)
	if err != nil {
		return nil, nil, err
	}
	if err := restrictStateDir(*f.dir); err != nil {
		return nil, nil, err
	}
	if len(st.Cert) > 0 {
		out.say(fmt.Sprintf("already enrolled as host %d", st.Config.HostID))
		return st, caPEM, nil
	}
	if *f.token != "" {
		st.Config.PendingToken = *f.token
		if err := st.SaveConfig(); err != nil {
			return nil, nil, err
		}
	}
	client, err := enroll.NewClient(st.Config.ServerURL, caPEM)
	if err != nil {
		return nil, nil, err
	}
	req, err := enrollRequest(st, st.Config.PendingToken, out)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	resp, err := enrollLoop(ctx, st, client, req, true, out)
	cancel()
	switch {
	case err != nil:
		out.say("the server did not answer; the service will keep trying")
	case resp.Status == enroll.StatusPending:
		out.say("the service will keep asking until an admin approves this host in Host Management")
	}
	return st, caPEM, nil
}
