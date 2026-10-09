# probe_rename_in_domain.ps1
#
# WHAT:   Asks whether a domain-joined Windows machine, running as SYSTEM with
#         NO domain credential, can rename its own computer object with
#         NetRenameMachineInDomain. Tries a rename to <name>X; on success it
#         renames straight back, so the machine ends where it started.
# WHERE:  telliottwin11 (10.255.25.1), joined to fogad.lab. Run as SYSTEM:
#           schtasks /create /f /tn renameprobe /sc once /st 00:00 /ru SYSTEM
#             /tr "powershell.exe -NoProfile -ExecutionPolicy Bypass -File C:\Windows\Temp\probe_rename_in_domain.ps1"
#           schtasks /run /tn renameprobe
#         Output: C:\Windows\Temp\rename-probe.txt
# WHY:    Design 0017. If the machine account may rename itself, no domain
#         credential ever has to reach a joined machine for a rename. If not,
#         the server must send the join credential for the rename.
# TRAP:   Running this in the ssh session tests the WRONG identity: that
#         session is a user, not the machine account. Only SYSTEM's network
#         identity is the computer account.
#         No reboot happens here. The active name never changes, and a
#         successful probe reverts the AD object before it exits.

$out = 'C:\Windows\Temp\rename-probe.txt'
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class NetRename {
    [DllImport("netapi32.dll", CharSet = CharSet.Unicode)]
    public static extern int NetRenameMachineInDomain(
        string lpServer, string lpNewMachineName,
        string lpAccount, string lpPassword, uint fRenameOptions);
}
'@

# NETSETUP_ACCT_CREATE: rename the computer account in the domain too.
$NETSETUP_ACCT_CREATE = 0x2
$cs = Get-CimInstance Win32_ComputerSystem
$old = $env:COMPUTERNAME
$new = $old + 'X'

"identity        : $([Security.Principal.WindowsIdentity]::GetCurrent().Name)" | Out-File $out
"computer        : $old  part_of_domain=$($cs.PartOfDomain) domain=$($cs.Domain)" | Out-File $out -Append
"secure channel  : $(Test-ComputerSecureChannel)" | Out-File $out -Append

$rc = [NetRename]::NetRenameMachineInDomain($null, $new, $null, $null, $NETSETUP_ACCT_CREATE)
"rename to $new (no credential): status $rc" | Out-File $out -Append

if ($rc -eq 0) {
    $back = [NetRename]::NetRenameMachineInDomain($null, $old, $null, $null, $NETSETUP_ACCT_CREATE)
    "rename back to $old           : status $back" | Out-File $out -Append
}
"secure channel  : $(Test-ComputerSecureChannel)" | Out-File $out -Append
