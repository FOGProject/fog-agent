package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

// whereToCheck is the one sentence that turns a fingerprint into a decision
// somebody can actually make. FOG 1.6 prints the same SHA-256, in the same
// upper-case colon-separated form, in the certificate table.
const whereToCheck = "Check it against the FOG web UI: FOG Configuration -> Certificates, the Root row's SHA-256 column."

// systemTrustNote says why a server on a public or corporate certificate
// has no fingerprint to check.
const systemTrustNote = "This machine already trusts the server's certificate for this name, and FOG's own CA did not issue it. " +
	"There is no fingerprint to compare: the agent checks the server the way a browser does, so a renewed certificate keeps working."

// probeCA is enroll.ProbeCA. A variable so a test can hand acquireCA the
// answer it decides about without standing up a TLS server.
var probeCA = enroll.ProbeCA

// cmdCA is the certificate side of setup, on its own so an admin can see
// what the installer would see before running it, and so a deployment
// script can read the fingerprint once and pin it on every machine.
func cmdCA(args []string) error {
	if len(args) == 0 || args[0] != "probe" {
		return errors.New("usage: fog-agent ca probe --server URL [--registry]")
	}
	var server string
	var registry bool
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--server":
			if i+1 >= len(rest) {
				return errors.New("--server needs a value")
			}
			i++
			server = rest[i]
		case "--registry":
			registry = true
		default:
			if v, ok := strings.CutPrefix(rest[i], "--server="); ok {
				server = v
				continue
			}
			return fmt.Errorf("unknown option %q", rest[i])
		}
	}
	if server == "" {
		return errors.New("fog-agent ca probe --server URL [--registry]")
	}

	if registry {
		// Before the probe, not after. The wizard reads these values back,
		// so a failed probe that left the previous server's answer behind
		// would put somebody in front of a fingerprint from a server they
		// are not installing against, and ask them to confirm it.
		if err := clearInstallerKeys(); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	probe, err := probeCA(ctx, server)
	if err != nil {
		if registry {
			// The wizard's failure dialog shows this, so the reason reaches
			// the person at the dialog and not only a log nobody opened.
			_ = writeInstallerError(err)
		}
		return err
	}
	fmt.Fprintf(logOut, "server:      %s\n", probe.ServerURL)
	if probe.SystemTrust {
		fmt.Fprintf(logOut, "trust:       this machine's certificate store, for this server name\n")
		fmt.Fprintf(logOut, "certificate: %s\n", probe.Subject)
		fmt.Fprintf(logOut, "issued by:   %s\n", probe.Issuer)
		fmt.Fprintf(logOut, "expires:     %s\n", probe.NotAfter.Format(time.RFC3339))
		fmt.Fprintf(logOut, "\n%s\n", systemTrustNote)
	} else {
		fmt.Fprintf(logOut, "certificate: %s\n", probe.Subject)
		fmt.Fprintf(logOut, "expires:     %s\n", probe.NotAfter.Format(time.RFC3339))
		fmt.Fprintf(logOut, "SHA-256:     %s\n", probe.Fingerprint)
		fmt.Fprintf(logOut, "\n%s\n", whereToCheck)
	}
	if registry {
		// The MSI wizard's only way to read a value back out of a program
		// it ran: Windows Installer cannot take a property from an exe
		// custom action, but a script custom action can read the registry.
		return writeInstallerKeys(probe)
	}
	return nil
}

// acquireCA settles what to trust when the command line named no CA file.
// It asks the server, and what happens next depends on the answer.
//
// A certificate issued by the CA the server publishes (FOG's own): the
// bundle is refused until something outside that connection agrees it is
// the right one -- --ca-fingerprint, which a deployment script knows in
// advance, or a person at the prompt who has the web UI open. Fetching
// alone would be trust on first use, which is what the old client did and
// what design 0002 rules out.
//
// A certificate this machine already trusts for the server's name (a public
// or corporate CA): that trust is the out-of-band decision, so nobody is
// asked, and the second result is true. A fingerprint given for such a
// server is refused, not dropped: whoever wrote it expected a pinned CA.
//
// No bundle, no system trust and no error means a FOG CA with nobody to
// confirm it; the caller's own error is the right one to report then.
func acquireCA(f commonFlags, serverURL string) ([]byte, bool, error) {
	want := strings.TrimSpace(*f.caFingerprint)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	probe, err := probeCA(ctx, serverURL)
	if err != nil {
		return nil, false, err
	}

	if probe.SystemTrust {
		if want != "" {
			return nil, false, fmt.Errorf(
				"this install was told to expect a CA with fingerprint %s, but the certificate %s presents "+
					"is not issued by the CA it publishes (it is issued by %s, which this machine trusts). "+
					"Nothing was trusted. To verify the server against this machine's trust store instead, "+
					"leave the fingerprint out",
				want, probe.ServerURL, probe.Issuer)
		}
		fmt.Fprintf(logOut, "%s presents a certificate this machine trusts (%s, issued by %s); "+
			"the agent verifies the server against this machine's trust store and the server name\n",
			probe.ServerURL, probe.Subject, probe.Issuer)
		return nil, true, nil
	}

	if want != "" {
		if !enroll.SameFingerprint(want, probe.Fingerprint) {
			return nil, false, fmt.Errorf(
				"the certificate %s publishes is not the one this install was told to expect.\n"+
					"  expected: %s\n"+
					"  offered:  %s\n"+
					"Nothing was trusted. Either the address is wrong, the server's certificate "+
					"has been replaced, or something is answering in its place",
				probe.ServerURL, want, probe.Fingerprint)
		}
		fmt.Fprintln(logOut, "certificate confirmed against the fingerprint given:", probe.Fingerprint)
		return probe.PEM, false, nil
	}
	if !stdinIsConsole() {
		return nil, false, nil
	}

	fmt.Fprintf(os.Stderr, "\n%s offers this certificate to trust:\n\n", probe.ServerURL)
	fmt.Fprintf(os.Stderr, "  %s\n", probe.Subject)
	fmt.Fprintf(os.Stderr, "  expires %s\n", probe.NotAfter.Format("2006-01-02"))
	fmt.Fprintf(os.Stderr, "  SHA-256 %s\n\n", probe.Fingerprint)
	fmt.Fprintf(os.Stderr, "%s\n", whereToCheck)
	answer := strings.ToLower(ask("Does that fingerprint match, character for character? [y/N]", ""))
	if answer != "y" && answer != "yes" {
		return nil, false, errors.New("the certificate was not confirmed, so nothing was trusted and nothing was installed")
	}
	return probe.PEM, false, nil
}
