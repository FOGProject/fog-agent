// Hands the certificate probe's answer to Windows Installer.
//
// `fog-agent ca probe --registry` leaves its answer under
// HKCU\Software\FOG\Setup, and the wizard's next page has to show it: a
// fingerprint to confirm (CAFINGERPRINT), a certificate this machine already
// trusts (CATRUST), or the reason the probe failed (CAERROR). Getting a value
// from a program into a property is the awkward part of Windows Installer:
// an EXE custom action cannot set one, and AppSearch -- which reads the
// registry and would have been the obvious answer -- cannot be reached,
// because the DoAction control event runs CUSTOM actions only and quietly
// does nothing when handed a standard action's name. That cost a round of
// testing: the probe ran, wrote the right value, and the wizard still
// reported that the server published no certificate.
//
// A script custom action can set properties directly, so it does.
//
// Every value is written on every run, empty when the key is absent, so a
// failed probe clears what the last one left rather than presenting the
// previous server's fingerprint for somebody to confirm.
function ReadProbe() {
    var shell = new ActiveXObject("WScript.Shell");
    var key = "HKCU\\Software\\FOG\\Setup\\";
    var names = ["CAFingerprint", "CATrust", "CASubject", "CAIssuer", "CAExpires", "CAError"];
    var props = ["CAFINGERPRINT", "CATRUST", "CASUBJECT", "CAISSUER", "CAEXPIRES", "CAERROR"];
    for (var i = 0; i < names.length; i++) {
        var value = "";
        try {
            value = "" + shell.RegRead(key + names[i]);
        } catch (e) {
            value = "";
        }
        Session.Property(props[i]) = value;
    }
    return 1; // msiDoActionStatusSuccess
}
