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
		// Before the probe, not after. The wizard reads these keys back
		// with AppSearch, so a failed probe that left the previous
		// server's answer behind would put somebody in front of a
		// fingerprint from a server they are not installing against, and
		// ask them to confirm it.
		if err := clearInstallerKeys(); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	probe, err := enroll.ProbeCA(ctx, server)
	if err != nil {
		return err
	}
	fmt.Fprintf(logOut, "server:      %s\n", probe.ServerURL)
	fmt.Fprintf(logOut, "certificate: %s\n", probe.Subject)
	fmt.Fprintf(logOut, "expires:     %s\n", probe.NotAfter.Format(time.RFC3339))
	fmt.Fprintf(logOut, "SHA-256:     %s\n", probe.Fingerprint)
	fmt.Fprintf(logOut, "\n%s\n", whereToCheck)
	if registry {
		// The MSI wizard's only way to read a value back out of a program
		// it ran: Windows Installer cannot take a property from an exe
		// custom action, but AppSearch can lift one out of the registry.
		return writeInstallerKeys(probe)
	}
	return nil
}

// acquireCA settles the trust anchor when the command line named no CA
// file. It fetches what the server publishes and then refuses to use it
// until something outside that connection agrees it is the right one:
// either --ca-fingerprint, which a deployment script knows in advance, or a
// person at the prompt who has the web UI open. Fetching alone would be
// trust on first use, which is what the old client did and what the trust
// model (design 0002) rules out.
//
// A nil bundle with no error means there was nobody to ask and nothing to
// check against; the caller's own "--ca is required" error is the right one
// to report then.
func acquireCA(f commonFlags, serverURL string) ([]byte, error) {
	want := strings.TrimSpace(*f.caFingerprint)
	if want == "" && !stdinIsConsole() {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	probe, err := enroll.ProbeCA(ctx, serverURL)
	if err != nil {
		return nil, err
	}

	if want != "" {
		if !enroll.SameFingerprint(want, probe.Fingerprint) {
			return nil, fmt.Errorf(
				"the certificate %s publishes is not the one this install was told to expect.\n"+
					"  expected: %s\n"+
					"  offered:  %s\n"+
					"Nothing was trusted. Either the address is wrong, the server's certificate "+
					"has been replaced, or something is answering in its place",
				probe.ServerURL, want, probe.Fingerprint)
		}
		fmt.Fprintln(logOut, "certificate confirmed against the fingerprint given:", probe.Fingerprint)
		return probe.PEM, nil
	}

	fmt.Fprintf(os.Stderr, "\n%s offers this certificate to trust:\n\n", probe.ServerURL)
	fmt.Fprintf(os.Stderr, "  %s\n", probe.Subject)
	fmt.Fprintf(os.Stderr, "  expires %s\n", probe.NotAfter.Format("2006-01-02"))
	fmt.Fprintf(os.Stderr, "  SHA-256 %s\n\n", probe.Fingerprint)
	fmt.Fprintf(os.Stderr, "%s\n", whereToCheck)
	answer := strings.ToLower(ask("Does that fingerprint match, character for character? [y/N]", ""))
	if answer != "y" && answer != "yes" {
		return nil, errors.New("the certificate was not confirmed, so nothing was trusted and nothing was installed")
	}
	return probe.PEM, nil
}
