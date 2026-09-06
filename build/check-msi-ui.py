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
DoAction names a CUSTOM action, which is the only kind it can run; every AppSearch
row has the locator it points at; every Bitmap control's image is an
uncompressed BMP; and every dialog's Control_Next pointers
form one closed loop starting at Control_First, which is what error 2834 is
about.

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
import re
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


def binary_streams(msi):
    """The Binary table's streams, by name. msiinfo writes them out as files
    under a Binary/ directory rather than to stdout."""
    out = {}
    with tempfile.TemporaryDirectory() as scratch:
        subprocess.run(["msiinfo", "export", os.path.abspath(msi), "Binary"],
                       capture_output=True, cwd=scratch)
        d = os.path.join(scratch, "Binary")
        for f in os.listdir(d) if os.path.isdir(d) else []:
            with open(os.path.join(d, f), "rb") as fh:
                out[f.removeprefix("Binary.")] = fh.read(64)
    return out


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

    # DoAction runs CUSTOM actions only. Handed the name of a standard
    # action it does nothing at all and reports nothing, which is how a
    # button that was supposed to run AppSearch shipped and looked, from the
    # outside, exactly like a server that published no certificate.
    custom = {r[0] for r in table(msi, "CustomAction")}
    for r in table(msi, "ControlEvent"):
        if r[2] == "DoAction" and r[3] not in custom:
            bad.append(f"ControlEvent {r[0]}.{r[1]} DoAction names {r[3]}, which is not a custom action "
                       f"(DoAction cannot run standard actions, and fails silently)")

    # Windows Installer walks Control_First and follows Control_Next, and
    # refuses the dialog with error 2834 unless that walk visits every
    # tabbable control and returns to where it started. wixl takes the FIRST
    # AUTHORED control as Control_First whether or not it is in the chain,
    # so marking the banner bitmap TabSkip and leaving it first in the file
    # is enough to break the loop -- which is exactly what shipped and what
    # 2834 said. Error dialogs (attribute bit 65536) are exempt: the engine
    # drives those itself.
    for r in dialogs:
        dlg = r[0]
        attrs = int(r[5] or 0)
        if attrs & 65536:
            continue
        cs = controls.get(dlg, {})
        chained = {c for c, row in cs.items() if len(row) > 10 and row[10]}
        first = r[7] if len(r) > 7 else ""
        if not chained:
            bad.append(f"Dialog {dlg} has no tab loop at all: no control has a Control_Next")
            continue
        if first not in chained:
            bad.append(f"Dialog {dlg}.Control_First is {first}, which is not in the tab loop (error 2834)")
            continue
        walk, cur = [], first
        while cur and cur not in walk:
            walk.append(cur)
            cur = cs[cur][10] if cur in cs and len(cs[cur]) > 10 else ""
        if cur != first or len(walk) != len(chained):
            bad.append(f"Dialog {dlg} tab order is not a single loop (error 2834): "
                       f"{' -> '.join(walk)} -> {cur or 'nothing'}, missing "
                       f"{sorted(chained - set(walk))}")

    # A control that runs off its dialog is error 2826. Not always fatal,
    # but it is always a layout mistake, and it is invisible from here
    # otherwise.
    for r in dialogs:
        dw, dh = int(r[3] or 0), int(r[4] or 0)
        for cid, c in controls.get(r[0], {}).items():
            x, y, w, h = (int(c[i] or 0) for i in (3, 4, 5, 6))
            if x + w > dw or y + h > dh:
                bad.append(f"Control {r[0]}.{cid} runs to {x + w}x{y + h}, past the dialog's {dw}x{dh} (error 2826)")

    # A leading {\Style} in a control's text names a TextStyle row; without
    # one the text silently renders in the fallback font.
    styles = {r[0] for r in table(msi, "TextStyle")}
    named = re.compile(r"^\{\\([^}]+)\}")
    for dlg, cs in controls.items():
        for cid, c in cs.items():
            m = named.match(c[9] if len(c) > 9 else "")
            if m and m.group(1) not in styles:
                bad.append(f"Control {dlg}.{cid} asks for text style {m.group(1)}, which has no TextStyle row")

    # A Bitmap control's image has to be an uncompressed BMP. Windows
    # Installer draws it with a loader that does not handle BI_RLE8, and a
    # compressed one comes out as the red broken-image square with no error
    # anywhere -- which is exactly what wixl's own ui extension ships, so
    # this is a live trap and not a hypothetical one.
    streams = binary_streams(msi)
    for dlg, cs in controls.items():
        for cid, c in cs.items():
            if c[2] != "Bitmap":
                continue
            head = streams.get(c[9] if len(c) > 9 else "", b"")
            if not head.startswith(b"BM") or len(head) < 34:
                bad.append(f"Control {dlg}.{cid} names Binary {c[9]}, which is not a BMP")
            elif int.from_bytes(head[30:34], "little") != 0:
                bad.append(f"Control {dlg}.{cid} names Binary {c[9]}, a compressed BMP; "
                           f"Windows Installer draws it as a broken-image square")

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
