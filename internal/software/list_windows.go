//go:build windows

package software

import (
	"golang.org/x/sys/windows/registry"
)

// uninstallPath is where Windows Installer, MSI-based installers and most
// third-party installers register what they put on the machine.
const uninstallPath = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`

// uninstallStore names one registry view to walk: a root, the access flags
// that select it, and the Arch a Program found there should carry.
type uninstallStore struct {
	root   registry.Key
	access uint32
	arch   string
}

// list walks the three uninstall stores Windows keeps program records in.
// HKLM has two views of the *same path*: WOW64_64KEY and WOW64_32KEY are not
// a copy-paste mistake, they are how the OS multiplexes 64-bit and 32-bit
// program registrations under one key name, and a 32-bit process would only
// see the 32-bit view without asking for the other explicitly. HKCU has only
// one view, for per-user installs.
func list() ([]Program, bool) {
	stores := []uninstallStore{
		{registry.LOCAL_MACHINE, registry.READ | registry.WOW64_64KEY, "x64"},
		{registry.LOCAL_MACHINE, registry.READ | registry.WOW64_32KEY, "x86"},
		{registry.CURRENT_USER, registry.READ, ""},
	}

	var out []Program
	seen := make(map[string]bool)
	opened := false

	for _, s := range stores {
		progs, ok := listStore(s)
		if !ok {
			// This view could not be opened (e.g. HKCU with no uninstall key
			// at all on a fresh profile). Not fatal on its own -- the other
			// views may still succeed -- so we keep going rather than bail.
			continue
		}
		opened = true
		for _, p := range progs {
			key := p.Name + "\x00" + p.Version + "\x00" + p.Source + "\x00" + p.Arch
			// A program can be visible in more than one view (rare, but MSI
			// components sometimes register in both WOW64 views), and it is
			// one installed program either way.
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, p)
		}
	}

	// false here means "no collector ran": every view failed to open, which
	// on a real Windows host means something is badly wrong (not merely
	// "zero programs installed"). List()'s caller must not report a list in
	// that case, because the server's reconcile marks anything missing from
	// a reported list as removed -- a false empty here would wipe the host's
	// whole software history over a permissions blip.
	if !opened {
		return nil, false
	}
	return out, true
}

// listStore opens one uninstall view and turns each qualifying subkey into a
// Program. The bool is false only when the view itself could not be opened.
func listStore(s uninstallStore) ([]Program, bool) {
	k, err := registry.OpenKey(s.root, uninstallPath, s.access)
	if err != nil {
		return nil, false
	}
	defer k.Close()

	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, false
	}

	var progs []Program
	for _, name := range names {
		p, ok := readUninstallEntry(s.root, s.access, name, s.arch)
		if ok {
			progs = append(progs, p)
		}
	}
	return progs, true
}

// readUninstallEntry reads one uninstall subkey and turns it into a Program,
// applying the filters that separate an actual installed program from the
// bookkeeping entries Windows and installers also leave in this key.
func readUninstallEntry(root registry.Key, access uint32, subkey, arch string) (Program, bool) {
	k, err := registry.OpenKey(root, uninstallPath+`\`+subkey, access)
	if err != nil {
		return Program{}, false
	}
	defer k.Close()

	name, _, err := k.GetStringValue("DisplayName")
	if err != nil || name == "" {
		// No DisplayName means no program a person would recognize -- most
		// commonly a patch or component entry, not something to report.
		return Program{}, false
	}

	if comp, _, err := k.GetIntegerValue("SystemComponent"); err == nil && comp == 1 {
		// Windows marks its own plumbing (runtimes, shared components) this
		// way so Programs and Features hides it; we hide it too.
		return Program{}, false
	}

	if parent, _, err := k.GetStringValue("ParentKeyName"); err == nil && parent != "" {
		// A non-empty ParentKeyName means this entry is an update to another
		// product's uninstall entry, not a product of its own.
		return Program{}, false
	}

	if rt, _, err := k.GetStringValue("ReleaseType"); err == nil {
		switch rt {
		case "Security Update", "Update Rollup", "Hotfix":
			// These ReleaseType values mark patches, not installed programs.
			return Program{}, false
		}
	}

	version, _, _ := k.GetStringValue("DisplayVersion")
	publisher, _, _ := k.GetStringValue("Publisher")
	installDate, _, _ := k.GetStringValue("InstallDate")

	return Program{
		Name:        name,
		Version:     version,
		Publisher:   publisher,
		Source:      "registry",
		Arch:        arch,
		InstallDate: registryDate(installDate),
	}, true
}
