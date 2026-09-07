//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

// The Windows service (design 0001 section 10.1: lifecycle through
// x/sys/windows/svc). `service install` is what the MSI will run when
// there is one; until then it is the installer, and it does what an
// installer must: put the binary somewhere stable, lock down the state
// directory, settle the server and CA, make the first enrollment request
// in front of the admin so a wrong URL, bundle or token fails right
// there, then register and start.
const (
	serviceName        = "fog-agent"
	serviceDisplay     = "FOG Agent"
	serviceDescription = "Enrolls this machine with its FOG server and keeps it the way the server says."
)

// recoveryActions is the restart policy the service must have: come back
// after a failure, backing off, with the counter resetting after a day up.
//
// It is what makes self-update work at all on Windows. The update path
// swaps the binary and exits non-zero ON PURPOSE so that the service
// manager starts the new one -- the same recovery path a crashing new
// binary takes (design 0015 section 6). With no recovery actions
// configured, Windows simply leaves the service stopped, and the machine
// sits with a new binary and nothing running until someone reboots it.
func recoveryActions() ([]mgr.RecoveryAction, uint32) {
	actions := make([]mgr.RecoveryAction, 0, len(restartDelays))
	for _, d := range restartDelays {
		actions = append(actions, mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: d})
	}
	return actions, uint32(restartResetWindow / time.Second)
}

// ensureRecoveryActions puts the restart policy back if it is missing,
// and reports whether it had to.
//
// `fog-agent install` has always set this. The MSI never did: its
// <ServiceInstall> registers the service through Windows Installer, and
// wixl (msitools 0.106, which is what builds our package on Linux) has no
// element for failure actions at all -- so every MSI-installed agent had
// an empty recovery policy and went dead on its first self-update.
// Observed on telliottwin11, 2026-09-07: binary swapped to 0.1.4, service
// STOPPED, `sc qfailure` empty, probation never cleared because nothing
// was running to clear it.
//
// Repairing it here rather than only in the installer is deliberate: it
// fixes the agents that are ALREADY installed, on their next start,
// without anyone reinstalling anything. It runs as LocalSystem, which
// holds SERVICE_CHANGE_CONFIG on its own service.
//
// Never fatal. An agent that cannot reconfigure itself should still run;
// it just has to say so, because the consequence is silent until the day
// an update lands.
func ensureRecoveryActions() (bool, error) {
	m, err := mgr.Connect()
	if err != nil {
		return false, err
	}
	defer m.Disconnect()
	svcH, err := m.OpenService(serviceName)
	if err != nil {
		return false, err
	}
	defer svcH.Close()

	changed := false

	want, reset := recoveryActions()
	// A service that already restarts on failure is left alone, whatever
	// the delays are: an admin may have tuned them, and stamping over
	// that every start would be this code deciding it knows better.
	restarts := false
	if have, err := svcH.RecoveryActions(); err == nil {
		for _, a := range have {
			if a.Type == mgr.ServiceRestart {
				restarts = true
				break
			}
		}
	}
	if !restarts {
		if err := svcH.SetRecoveryActions(want, reset); err != nil {
			return false, err
		}
		changed = true
	}

	// And the flag without which none of the above ever runs.
	//
	// Windows applies recovery actions only when a service terminates
	// UNEXPECTEDLY. Self-update does not: it finishes cleanly and reports
	// Stopped with a non-zero service-specific exit code, which Windows
	// calls a "non-crash failure" and ignores unless this flag is set.
	// So a service could have a perfectly good restart policy and still
	// sit stopped after every update -- which is exactly what happened on
	// telliottwin11 with 0.1.5: policy present, service STOPPED.
	//
	// This is also why killing the process was the wrong way to test it.
	// taskkill IS a crash, so it fires the actions whether or not the flag
	// is set, and proves nothing about the path an update takes.
	//
	// Checked independently of the actions above: an admin who set a
	// restart policy by hand almost certainly did not know to set this.
	if on, err := svcH.RecoveryActionsOnNonCrashFailures(); err == nil && !on {
		if err := svcH.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

// isService reports whether the service control manager started us.
func isService() bool {
	ok, _ := svc.IsWindowsService()
	return ok
}

// runService is the process under the SCM: stderr does not exist there,
// so the log file stands in before anything is said.
func runService() error {
	dir := dirArg(os.Args[2:])
	lf, err := openLog(dir)
	if err != nil {
		// Nowhere else to say it: the event log is the only channel
		// left, and it exists if install registered the source.
		if el, e := eventlog.Open(serviceName); e == nil {
			el.Error(1, "fog-agent cannot open its log: "+err.Error())
			el.Close()
		}
		return err
	}
	defer lf.Close()
	logOut = lf
	h := &handler{args: os.Args[2:]}
	if el, err := eventlog.Open(serviceName); err == nil {
		h.events = el
		defer el.Close()
	}
	return svc.Run(serviceName, h)
}

// handler runs the agent loop and answers the SCM. Stop and shutdown
// cancel the loop's context and wait for it, so a converge in progress
// finishes its report before the process goes.
type handler struct {
	args   []string
	events *eventlog.Log
}

func (h *handler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runAgent(ctx, h.args) }()
	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	h.note("started")
	if fixed, err := ensureRecoveryActions(); err != nil {
		h.note("could not read or set the restart policy (" + err.Error() +
			"); a self-update will leave this service stopped until it is started again")
	} else if fixed {
		h.note("restart policy was missing and has been set; this service " +
			"can now come back on its own after a self-update")
	}
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				h.note("stopped")
				return false, 0
			}
		case err := <-done:
			// A planned restart is not a failure. The update path
			// replaced this binary on purpose and returns non-zero so
			// the recovery actions start the new one -- the exit code
			// is the mechanism, not a fault. Reporting it as "fatal"
			// and writing an Error to the event log said the update had
			// crashed when it had just succeeded, and any monitoring
			// watching that log would have alerted on every update.
			if errors.Is(err, errUpdated) {
				h.note("replaced itself; restarting into the new binary")
				return true, 1
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				// A fatal error (state directory unreadable, no server
				// configured) exits non-zero so the recovery actions
				// set at install restart the service.
				fmt.Fprintln(logOut, time.Now().Format(time.RFC3339), "fatal:", err)
				if h.events != nil {
					h.events.Error(1, "fog-agent stopped: "+err.Error())
				}
				return true, 1
			}
			return false, 0
		}
	}
}

func (h *handler) note(msg string) {
	fmt.Fprintln(logOut, time.Now().Format(time.RFC3339), "service", msg)
	if h.events != nil {
		h.events.Info(1, "fog-agent "+msg)
	}
}

func cmdService(args []string) error {
	if len(args) == 0 {
		return errors.New("service needs install, uninstall, start, stop or status")
	}
	switch args[0] {
	case "install":
		return serviceInstall(args[1:])
	case "uninstall":
		return serviceUninstall()
	case "start":
		return serviceStart()
	case "stop":
		return serviceStop()
	case "status":
		return serviceStatus()
	}
	return fmt.Errorf("service: unknown command %q", args[0])
}

func serviceInstall(args []string) error {
	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	f := addCommonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager: %w (run from an administrator prompt)", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(serviceName); err == nil {
		s.Close()
		return errors.New("the service is already installed; `service uninstall` first")
	}
	// The state directory and the first enrollment attempt are the same
	// work the MSI does through `setup`; only the service registration
	// below is particular to a hand install.
	out := &sayer{}
	if _, _, err := prepareState(f, out); err != nil {
		return err
	}
	if err := postSetup(); err != nil {
		return err
	}
	exe, err := installBinary()
	if err != nil {
		return err
	}
	svcArgs := []string{"run"}
	if *f.dir != enroll.DefaultDir {
		svcArgs = append(svcArgs, "--dir", *f.dir)
	}
	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName: serviceDisplay,
		Description: serviceDescription,
		StartType:   mgr.StartAutomatic,
	}, svcArgs...)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()
	// Restart on failure, backing off; the counter resets after a day up.
	// Same policy the service repairs for itself at start, because the
	// MSI cannot express this and installs without it.
	ra, reset := recoveryActions()
	if err := s.SetRecoveryActions(ra, reset); err != nil {
		return fmt.Errorf("recovery actions: %w", err)
	}
	// Without this the actions above never run for a self-update: Windows
	// only applies them to an unexpected termination, and the update path
	// exits cleanly with a non-zero code. See ensureRecoveryActions.
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("recovery on non-crash failures: %w", err)
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	fmt.Fprintf(logOut, "installed and started %s from %s; log at %s\n", serviceName, exe, logPath(*f.dir))
	return nil
}

// installBinary copies this executable to Program Files\FOG unless it is
// already running from there, and returns the path the service runs.
func installBinary() (string, error) {
	src, err := os.Executable()
	if err != nil {
		return "", err
	}
	if src, err = filepath.EvalSymlinks(src); err != nil {
		return "", err
	}
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	dst := filepath.Join(pf, "FOG", "fog-agent.exe")
	if strings.EqualFold(src, dst) {
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp := dst + ".new"
	w, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(w, in); err != nil {
		w.Close()
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// restrictDir replaces the state directory's ACL with full control for
// SYSTEM (S-1-5-18) and Administrators (S-1-5-32-544), by SID so the
// names need no localizing, inherited by everything written under it.
func restrictDir(dir string) error {
	cmd := exec.Command("icacls", dir, "/inheritance:r",
		"/grant:r", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("icacls %s: %w\n%s", dir, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func openService() (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, fmt.Errorf("service manager: %w (run from an administrator prompt)", err)
	}
	s, err := m.OpenService(serviceName)
	if err != nil {
		m.Disconnect()
		return nil, nil, errors.New("the service is not installed")
	}
	return m, s, nil
}

// serviceUninstall stops and removes the service. The binary and the
// state directory stay: the key and certificate are this machine's
// identity to the server, and a reinstall picks them up.
func serviceUninstall() error {
	m, s, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	if err := stopAndWait(s); err != nil {
		return err
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	eventlog.Remove(serviceName)
	fmt.Fprintf(logOut, "removed %s; the state in %s and the binary are left in place\n", serviceName, enroll.DefaultDir)
	return nil
}

func serviceStart() error {
	m, s, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	return s.Start()
}

func serviceStop() error {
	m, s, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	return stopAndWait(s)
}

// stopAndWait asks a running service to stop and waits for it to be so;
// a service that is not running needs nothing.
func stopAndWait(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if st, err = s.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for st.State != svc.Stopped {
		if time.Now().After(deadline) {
			return errors.New("the service did not stop within 30s")
		}
		time.Sleep(300 * time.Millisecond)
		if st, err = s.Query(); err != nil {
			return err
		}
	}
	return nil
}

func serviceStatus() error {
	m, s, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return err
	}
	states := map[svc.State]string{
		svc.Stopped: "stopped", svc.StartPending: "starting", svc.StopPending: "stopping",
		svc.Running: "running", svc.ContinuePending: "resuming", svc.PausePending: "pausing", svc.Paused: "paused",
	}
	name := states[st.State]
	if name == "" {
		name = fmt.Sprintf("state %d", st.State)
	}
	fmt.Fprintf(logOut, "%s: %s (pid %d)\n", serviceName, name, st.ProcessId)
	return nil
}

// restrictStateDir is the ACL cut for the state directory, done before
// the key is ever written into it, since ProgramData lets every user read
// what others create there.
func restrictStateDir(dir string) error { return restrictDir(dir) }

// postSetup registers the event log source the service falls back to
// when it cannot open its log file. Both install paths need it.
func postSetup() error {
	if err := eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("event log source: %w", err)
	}
	return nil
}

// setupLog is the service log, opened for setup's own lines: under
// msiexec nothing else shows them.
func setupLog(dir string) (*os.File, error) { return openLog(dir) }
