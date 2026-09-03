# Installing the agent on Windows

The agent runs as a Windows service named `fog-agent` (display name "FOG
Agent") under the SYSTEM account. Until there is an MSI, the binary is its own
installer. From an administrator prompt:

```
fog-agent.exe service install --server https://fog.example.org/fog --ca ca.pem [--token T]
```

What `service install` does, in order:

- settles the server URL and the CA bundle in `%ProgramData%\FOG\agent` and
  cuts that directory's ACL down to SYSTEM and Administrators, before the key
  is generated, because ProgramData lets every user read what others create
- copies the binary to `%ProgramFiles%\FOG\fog-agent.exe` unless it is
  already running from there
- makes the first enrollment request in front of you: a wrong URL, bundle or
  token fails here and nothing is registered. With a token the host is
  enrolled on the spot; without one it is pending and the service keeps
  asking until an admin approves it in Host Management
- registers the service (automatic start, restart on failure at 10 s, 1 min,
  5 min), an event log source, and starts it

The service writes `%ProgramData%\FOG\agent\agent.log` (rolled to `.1` past
1 MB) and start, stop and fatal errors to the Application event log.

A host with software assigned but no Chocolatey reports every entry as
"cannot run" and checks for it at each poll. Set
`FOG_SOFTWARE_CHOCO_BOOTSTRAP_URL` on the server (FOG Configuration, FOG
Client) to have the agent install Chocolatey itself from that script.

Other commands: `service status`, `service stop`, `service start`,
`service uninstall`. Uninstall removes the service and leaves the state
directory (this machine's key and certificate) and the binary in place, so a
reinstall picks the same identity up.
