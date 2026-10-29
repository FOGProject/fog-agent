# FOG Agent

The FOG Agent is the management agent for [FOG Project](https://fogproject.org)
1.6. It replaces the legacy `fog-client` ("FOG Service").

The agent is one static Go binary for each OS and architecture. It needs no
runtime. It connects outbound only, over HTTPS, to the FOG server that the
machine is enrolled with.

> **Status:** pre-1.0. Windows is the supported platform, with an MSI. Linux
> and macOS binaries are built for each release, but they have no package yet.

## How it works

The server holds a desired state for each host. The agent fetches that state,
reads the actual state of the machine, and makes the two match. Then it reports
the result and any drift to the server.

| The legacy client | The FOG Agent |
|---|---|
| Needs .NET Framework, or Mono on Linux and macOS | One static binary, no runtime |
| Identifies the host by MAC address only | Identifies the host by its SMBIOS identity, then by MAC |
| Hand-rolled RSA and AES over plain HTTP | Mutual TLS: a key for each host, and a certificate from FOG's own CA |
| Runs each task when told, and does not check the machine | Reads the machine's state and changes only what differs |
| Updates itself silently from any FOG server | Checks a signed central manifest, and updates only when an admin approves |

The agent takes orders only from its enrolled server. It sends no telemetry.
See [PRIVACY.md](PRIVACY.md) for what it reports, and to where.

## What it manages

| Area | What the agent does |
|---|---|
| Hostname | Renames the machine, also inside a domain |
| Directory membership | Joins Active Directory, and reuses the computer object of a reimaged machine |
| Software | Installs and removes packages through Chocolatey on Windows |
| Snapins | Runs one-off scripts and installers |
| Printers | Adds and removes printers |
| Power | Shuts down and reboots on a schedule or on demand |
| Auto log out | Logs out an idle user |
| Product key | Applies and activates a Windows product key |
| Inventory | Reports hardware, installed software, and Secure Boot state |
| User tracking | Reports logon sessions |
| Netboot | Arms a one-time network boot for an imaging task |
| Wake relay | Sends Wake-on-LAN for hosts on its own subnet |
| Self-update | Applies a signed release when an admin approves it |

The design documents in [`docs/design/`](docs/design/) explain each area.

## Requirements

- A FOG server on 1.6, which serves the agent API at `/fog/agent/v1`.
- For the MSI, 64-bit Windows. Each release also carries Windows binaries for
  x86 and arm64, and binaries for Linux and macOS.

## Install on Windows

Download the MSI from the [latest release](https://github.com/FOGProject/fog-agent/releases/latest).
Double-click it. The wizard asks for the server address and an optional
enrollment token. Then it shows the certificate that the agent will trust.

The server address is the address of the FOG web UI, with `/fog` on the end.
An example is `https://fog.example.org/fog`.

**Compare the fingerprint before you click Next.** In the FOG web UI, open
**FOG Configuration**, then the **Certificates** tab. The **Root** row shows
the same SHA-256 value. If the two values do not match, click Cancel.

For a silent install, give the fingerprint on the command line:

```
msiexec /i fog-agent-0.1.18-x64.msi /qn SERVER=https://fog.example.org/fog CAFINGERPRINT=50:02:7A:... /l*v C:\fog-agent-install.log
```

To read the fingerprint from any machine, run:

```
fog-agent.exe ca probe --server https://fog.example.org/fog
```

The MSI removes the legacy FOG Service if it finds one. Without a `TOKEN`,
the host waits under **Hosts > Pending Agents** until an admin approves it.

[`docs/install-windows.md`](docs/install-windows.md) has the full procedure.
It covers servers on a public or corporate certificate, every MSI property,
the hand install, and image capture.

## Run on Linux or macOS

There is no package yet. Run the release binary from a systemd or launchd
unit, as root:

```
fog-agent run --server https://fog.example.org/fog --ca /path/to/ca.cert.pem
```

The state directory is `/var/lib/fog-agent` on Linux and
`/Library/Application Support/fog-agent` on macOS.

## Commands

| Command | What it does |
|---|---|
| `fog-agent identity` | Prints the SMBIOS identity and the MAC addresses |
| `fog-agent run` | Enrolls if necessary, then polls the server |
| `fog-agent status` | Shows the enrollment state |
| `fog-agent ca probe --server URL` | Shows what the agent would trust for the server |
| `fog-agent renew` | Renews the certificate now |
| `fog-agent update --to VERSION` | Verifies and applies a version now |
| `fog-agent update-revert` | Puts back the binary that an update replaced |
| `fog-agent service install\|uninstall\|start\|stop\|status` | Manages the Windows service |
| `fog-agent version` | Prints the version |

## Logs

On Windows, the service writes `C:\ProgramData\FOG\fog-agent.log`. Attach that
file when you ask for help on the [FOG forums](https://forums.fogproject.org).
The log holds no key, certificate, or token. On Linux and macOS, the service
unit captures the log.

## Build from source

You need Go at the version in [`go.mod`](go.mod).

```
go test ./... -count=1
go build -o bin/fog-agent ./cmd/fog-agent
```

`build/cross.sh` builds every target into `dist/`. `build/msi.sh` builds the
MSI on Linux with `wixl` from msitools. No Windows machine is necessary.

[`docs/RELEASING.md`](docs/RELEASING.md) describes how a release is cut and
signed. [`docs/signing/`](docs/signing/) describes the code-signing policy.

## License

GPL-3.0. See [LICENSE](LICENSE).
