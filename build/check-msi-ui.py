#!/usr/bin/env python3
"""Check that every UI reference in a built MSI resolves.

build/msi.sh runs this after wixl. wixl pulls into the package only what is
referenced, and it does not check the other direction: a stock dialog that
spawns another one it was never told to include compiles clean and fails at
the moment the spawn happens. That is how OutOfDiskDlg and OutOfRbDiskDlg
went missing -- VerifyReadyDlg and ResumeDlg both spawn them when the volume
is short of space, and wixl's own WixUI_Minimal does not reference them
either.

Checked: Dialog.Control_First/Default/Cancel and Control.Control_Next name a
control that exists in that dialog; Bitmap and Icon controls name a Binary
row that exists; every ControlEvent and EventMapping row names a real
control; every NewDialog/SpawnDialog names a dialog in the package; every
DoAction names an action that exists, custom or sequenced; and every
AppSearch row has the locator it points at.

The DoAction check earns its keep on this package specifically: wixl accepts
BinaryKey only with DllEntry or JScriptCall, so the wizard's ProbeCA action
(BinaryKey with ExeCommand) trips an assertion and leaves no row, and
build/msi.sh appends it afterwards with msibuild. A button publishing
DoAction against a name with no row is not an error to Windows Installer --
it just does nothing, and the wizard would go on to report that the server
published no certificate.

Exits non-zero, listing what dangles, so a broken wizard cannot ship.
"""
import os
import subprocess
import sys
import tempfile


def table(msi, name):
    # cwd is a scratch directory because msiinfo writes a table's binary
    # streams out as files beside itself (exporting Binary leaves a
    # Binary/ directory), and this runs from the repository root.
    with tempfile.TemporaryDirectory() as scratch:
        out = subprocess.run(["msiinfo", "export", os.path.abspath(msi), name],
                             capture_output=True, text=True, cwd=scratch)
    if out.returncode != 0:  # a table the package does not have at all
        return []
    # msiinfo emits two header lines (names, types) and one table-name line.
    return [ln.split("\t") for ln in out.stdout.split("\n")[3:] if ln.strip()]


def main(msi):
    dialogs = table(msi, "Dialog")
    dialog_ids = {r[0] for r in dialogs}
    controls = {}
    for r in table(msi, "Control"):
        controls.setdefault(r[0], {})[r[1]] = r
    binaries = {r[0] for r in table(msi, "Binary")}

    bad = []
    for r in dialogs:
        for idx, label in ((7, "Control_First"), (8, "Control_Default"), (9, "Control_Cancel")):
            v = r[idx] if len(r) > idx else ""
            if v and v not in controls.get(r[0], {}):
                bad.append(f"Dialog {r[0]}.{label} names control {v}, which does not exist")
    for dlg, cs in controls.items():
        for cid, r in cs.items():
            nxt = r[10] if len(r) > 10 else ""
            if nxt and nxt not in cs:
                bad.append(f"Control {dlg}.{cid}.Control_Next names {nxt}, which does not exist")
            if r[2] in ("Bitmap", "Icon"):
                art = r[9] if len(r) > 9 else ""
                if art and art not in binaries:
                    bad.append(f"Control {dlg}.{cid} ({r[2]}) names Binary {art}, which is not in the package")
    for r in table(msi, "ControlEvent"):
        if r[1] not in controls.get(r[0], {}):
            bad.append(f"ControlEvent {r[0]}.{r[1]} names a control that does not exist")
        if r[2] in ("NewDialog", "SpawnDialog") and r[3] not in dialog_ids:
            bad.append(f"ControlEvent {r[0]}.{r[1]} {r[2]} names dialog {r[3]}, which is not in the package")
    for r in table(msi, "EventMapping"):
        if r[1] not in controls.get(r[0], {}):
            bad.append(f"EventMapping {r[0]}.{r[1]} names a control that does not exist")

    # An action a button can invoke is either a custom action or one of the
    # standard actions this package sequences; both are named in tables, so
    # nothing has to be hard-coded.
    actions = {r[0] for r in table(msi, "CustomAction")}
    for seq in ("InstallUISequence", "InstallExecuteSequence", "AdminUISequence"):
        actions |= {r[0] for r in table(msi, seq)}
    for r in table(msi, "ControlEvent"):
        if r[2] == "DoAction" and r[3] not in actions:
            bad.append(f"ControlEvent {r[0]}.{r[1]} DoAction names {r[3]}, which is neither a custom action nor sequenced")

    locators = set()
    for t in ("RegLocator", "IniLocator", "CompLocator", "DrLocator", "Signature"):
        locators |= {r[0] for r in table(msi, t)}
    for r in table(msi, "AppSearch"):
        if r[1] not in locators:
            bad.append(f"AppSearch for {r[0]} names locator {r[1]}, which does not exist")

    if bad:
        print("build/check-msi-ui.py: the wizard has dangling references:", file=sys.stderr)
        for line in sorted(set(bad)):
            print("  " + line, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1]))
