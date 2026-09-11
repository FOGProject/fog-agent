package main

import (
	"errors"
	"io/fs"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"golang.org/x/sys/windows/registry"
)

// installerKey is where `ca probe --registry` leaves what the MSI wizard
// then reads with its ReadProbe script. HKCU, not HKLM: the wizard's
// dialogs run unelevated in the person's own session, and these values are
// not a secret or a decision -- they are on their way to a dialog that asks
// somebody to look at them.
const installerKey = `Software\FOG\Setup`

// clearInstallerKeys removes last time's answer. Deleting the whole key is
// simpler than deleting each value and leaves nothing to find, which is
// the state a failed probe has to end in.
func clearInstallerKeys() error {
	err := registry.DeleteKey(registry.CURRENT_USER, installerKey)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// writeInstallerKeys hands the probe back to Windows Installer. An exe
// custom action cannot set a property; the registry is the way across, so
// the values land here under names build/msi/readprobe.js matches. Which
// page the wizard shows next depends on which of CAFingerprint and CATrust
// is set, so exactly one of them is.
func writeInstallerKeys(p *enroll.CAProbe) error {
	values := map[string]string{
		"CASubject": p.Subject,
		"CAExpires": p.NotAfter.Format("2006-01-02"),
	}
	if p.SystemTrust {
		values["CATrust"] = "system"
		values["CAIssuer"] = p.Issuer
	} else {
		values["CAFingerprint"] = p.Fingerprint
	}
	return setInstallerValues(values)
}

// writeInstallerError leaves the probe's reason for the wizard's failure
// dialog, which could otherwise only guess at it.
func writeInstallerError(err error) error {
	return setInstallerValues(map[string]string{"CAError": err.Error()})
}

func setInstallerValues(values map[string]string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, installerKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	for name, value := range values {
		if err := k.SetStringValue(name, value); err != nil {
			return err
		}
	}
	return nil
}
