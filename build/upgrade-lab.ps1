# Upgrades the FOG Agent on the Windows lab VM in place.
#
#   WHAT:  major-upgrades C:\Program Files\FOG\fog-agent.exe from an MSI
#          already staged in C:\Windows\Temp, then reports what is actually
#          on disk and running -- not what the install claimed.
#   WHERE: telliottwin11 (10.255.25.1), FOG host 105.
#   WHY:   an ssh session for an admin account on this box is UAC-filtered,
#          so msiexec cannot elevate from there. This file is run by a
#          scheduled task with /ru SYSTEM, which can.
#   NEEDS: the MSI at C:\Windows\Temp\fog-agent.msi. Enrollment survives the
#          upgrade: the certificate and state live in C:\ProgramData\FOG\agent,
#          which the MSI does not touch.
#   TRAP:  msiexec returns immediately unless it is waited on, and a
#          scheduled task that ends before the install does looks like a
#          success. Start-Process -Wait is what makes the exit code mean
#          anything.
#
# Log: C:\Windows\Temp\fog-agent-upgrade.log (this script) and
#      C:\Windows\Temp\fog-agent-msi.log (Windows Installer's own).

$ErrorActionPreference = 'Stop'
$log = 'C:\Windows\Temp\fog-agent-upgrade.log'

function Note($msg) {
    "$((Get-Date).ToString('s'))  $msg" | Tee-Object -FilePath $log -Append
}

Note '--- upgrade starting'

$msi = 'C:\Windows\Temp\fog-agent.msi'
if (-not (Test-Path $msi)) {
    Note "MISSING $msi"
    exit 1
}

$exe = 'C:\Program Files\FOG\fog-agent.exe'
if (Test-Path $exe) {
    Note ("before: " + (& $exe version 2>&1))
}

$p = Start-Process msiexec.exe -Wait -PassThru -ArgumentList @(
    '/i', $msi, '/qn',
    '/l*v', 'C:\Windows\Temp\fog-agent-msi.log'
)
Note "msiexec exit $($p.ExitCode)"
if ($p.ExitCode -ne 0) {
    exit $p.ExitCode
}

# Read the result rather than reporting the intention. A zero exit from
# msiexec says the transaction committed, not that the new binary is the one
# the service is now running.
Note ("after:  " + (& $exe version 2>&1))
Note ("file:   " + (Get-Item $exe).LastWriteTime.ToString('s'))

$svc = Get-Service fog-agent -ErrorAction SilentlyContinue
if ($null -eq $svc) {
    Note 'SERVICE MISSING'
    exit 1
}
if ($svc.Status -ne 'Running') {
    Note "service $($svc.Status); starting"
    Start-Service fog-agent
    Start-Sleep -Seconds 3
    $svc = Get-Service fog-agent
}
Note "service: $($svc.Status)"
Note '--- upgrade done'
