package main

import (
	"errors"
	"io/fs"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"golang.org/x/sys/windows/registry"
)

// installerKey is where `ca probe --registry` leaves what the MSI wizard
// then reads with AppSearch. HKCU, not HKLM: the wizard's dialogs run
// unelevated in the person's own session, and this value is not a secret
// or a decision -- it is a fingerprint on its way to a dialog that asks
// somebody to look at it.
const installerKey = `Software\FOG\Setup`

// clearInstallerKeys removes last time's answer. Deleting the whole key is
// simpler than deleting three values and leaves nothing for AppSearch to
// find, which is the state a failed probe has to end in.
func clearInstallerKeys() error {
	err := registry.DeleteKey(registry.CURRENT_USER, installerKey)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// writeInstallerKeys hands the probe back to Windows Installer. An exe
// custom action cannot set a property; AppSearch reading the registry is
// the way across, so the values land here under names the wxs matches.
func writeInstallerKeys(p *enroll.CAProbe) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, installerKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	for name, value := range map[string]string{
		"CAFingerprint": p.Fingerprint,
		"CASubject":     p.Subject,
		"CAExpires":     p.NotAfter.Format("2006-01-02"),
	} {
		if err := k.SetStringValue(name, value); err != nil {
			return err
		}
	}
	return nil
}
