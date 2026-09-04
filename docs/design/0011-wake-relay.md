# 0011: Waking a machine that no FOG server can reach

Status: SHIPPED 2026-09-04. Agent `internal/network` (the fact) and
`internal/provider/wake` (the sender); server `FOG\Agent\NetworkFacts`,
`FOG\Agent\WakeRelay`, schema 429 (`hostNetwork`) and 430 (`agentWake`,
`FOG_AGENT_WAKE_RELAY_ENABLED`).

Wake-on-LAN is a broadcast to a link. FOG sends it from the machines it
happens to own — the server and its storage nodes — so it can wake a sleeping
host only if one of those sits on the same link. In a routed estate that is
often not true, and there is nothing FOG can do about it today.

This document proposes letting an **already-awake agent** send the packet for
a machine on its own link, and constrains that hard: an agent will only ever
wake a machine FOG already knows about, and it is told which one by the
server rather than deciding for itself.

---

## 1. What is there today

### 1.1 There is already a relay, and it is a good one

The thing worth saying first, because it changes what is missing:
`FOGBase::wakeUp()` (`packages/web/src/Base/FOGBase.php:4389-4447`) does not
send a magic packet at all. It builds a list of every enabled, online
`storagenode` plus the master's own web host, and POSTs
`{'mac': …}` to `management/index.php?node=client&sub=wakeEmUp` on each of
them, authenticated with an `X-FOG-Node-Secret` header
(`FOGBase::nodeSecret()`, `:4368-4381`).

Each node's `FOGPage::wakeEmUp()` (`packages/web/src/Base/FOGPage.php:3967-3990`)
checks that header and then runs `WakeOnLan::send()` locally, which builds the
102-byte packet in `MACAddress::setMAC()`
(`packages/web/src/Items/MACAddress.php:154-158`) and sends it to
`255.255.255.255` plus every local broadcast address `FOGBase::getBroadcast()`
found from `ip -4 addr` (`:4655-4679`), UDP port 9, `SO_BROADCAST` set.

So FOG already fans a wake out to every link where it owns a machine. The
`wolbroadcast` plugin extends that with a hand-maintained list of extra
broadcast addresses, merged in through the `BROADCAST_ADDR` hook
(`lib/plugins/wolbroadcast/src/Hooks/AddBroadcastAddresses.php:71-82`).

### 1.2 What that leaves out

A directed broadcast to another subnet — which is what the `wolbroadcast`
plugin's extra addresses are — requires the routers between here and there to
forward it. `ip directed-broadcast` has been **off by default on Cisco IOS
since 12.0**, and is off by default on essentially every enterprise platform
since, because forwarding directed broadcasts is what made the smurf
amplification attack work. An admin can turn it back on. Most security teams
will decline, and they are right to.

That is the actual gap: **a subnet with FOG hosts on it but no FOG server or
storage node cannot be woken**, and the workaround FOG documents asks the
network team to re-enable a feature disabled for a good reason two decades
ago.

Which is to say the missing sender is not a server. It is a machine that is
already on that link, already awake, already talking to FOG, and already
authenticated — and every such subnet has one, or FOG would not know there
were hosts there.

### 1.3 The legacy client cannot help

Checked rather than assumed: a full grep of all 131 `.cs` files in
`/home/telliott/fog-client` for `WakeOn`, `magic`, `UdpClient` and `wol`
returns nothing. `Modules/PowerManagement/PowerManagement.cs` handles only
`shutdown` and `reboot`. The server side agrees and says why —
`Client/PM.php:72-87` explicitly strips `wol` out of the schedule handed to
the client, because a sleeping machine cannot ask for anything.

That reasoning is right about the *sleeping* machine and says nothing about
its neighbors. This design is about the neighbors.

### 1.4 The MAC list is not filtered

`Host::getMyMacs()` (`packages/web/src/Items/Host.php:2259-2266`) returns
every `hostMAC` row for a host with no filter on `hmPending`,
`hmIgnoreClient` or `hmIgnoreImaging`, and `Host::wakeOnLAN()` sends to all of
them. `Group::wakeOnLAN()` does filter `pending`. Worth knowing, and §5 says
what this design does about it.

---

## 2. The model: the server picks the sender and the target

The one thing that must not happen is an endpoint that takes a MAC address
and shouts it at the network. A magic packet is unauthenticated by
construction — that is the whole protocol — so the control has to be on
**who may ask** and **what may be asked for**, and both of those live on the
server.

So:

- The server decides that host A should be woken.
- The server decides that agent B is on A's link and is awake.
- The server tells B, in its ordinary poll answer, "send a wake for this
  MAC". B has already authenticated with its client certificate to get that
  answer.
- B sends the packet to its own local broadcast and reports that it did.

The agent is a **sender, not a decider**. It never chooses a target, never
accepts one from anywhere but the poll answer, and never accepts an arbitrary
address to send to.

### Why not have the agent listen for a wake request

Because then the agent has an inbound port, and anything that can reach it
can ask for a broadcast. Every argument for that shape ends in
"…and we authenticate it", at which point it is a second authenticated
channel doing what the existing one already does, on a schedule the server
does not control. The poll answer is the channel; a wake simply rides it.

The cost is latency: a wake waits up to one poll interval. §6 says what to do
about that and why it is not urgent.

### Why not just ask for directed broadcast

Covered in §1.2. It is a real answer for an estate whose network team will do
it, and the `wolbroadcast` plugin already serves that estate. This is for the
much larger set who will not, and it needs no network change at all.

---

## 3. Which agent is on which link

This is the only genuinely new question, and FOG can already answer it from
data it has.

`hostIP` is not enough — it is whatever the host last resolved to and may be
stale or absent, with no prefix and no notion of which of several interfaces
it came from. **FOG has never recorded a host's interfaces at all**: this was
drafted believing the inventory block already carried them and it does not,
so the link data is a new fact kind (`network`) rather than an extra field on
an existing one — a `FACT_REPORTS` entry and a block in the poll, never a new
route.

An address plus a prefix length is a link, and two machines whose interface
addresses fall in the same network with the same prefix are on the same link.
`hostNetwork` holds one row per host per interface *address* (an interface
with two addresses is on two links and can broadcast on both), and it stores
the **computed network address alongside the prefix**. That is the whole
reason the table earns its place: finding a link's other members is then an
index lookup on two columns, where the honest alternative —
`INET_ATON(hnIPv4) & mask` — is a full scan on every wake.

The fact is gathered on **every poll**, not on the hourly `FactsInterval` the
other facts use. It is a single `net.Interfaces()` call where enumerating a
package-managed host's packages is the most expensive thing the agent does,
and staleness here costs a wake: an hour of it is an hour of the server
asking a laptop that has gone home to broadcast on a subnet it left. It still
only goes on the wire when its hash moves.

It is also the one fact with **no per-platform file**. `net.Interfaces()` is
one of the few places the Go runtime already does the platform work —
`GetAdaptersAddresses` on Windows, netlink on Linux, `getifaddrs` on the BSDs
— so a `gather_windows.go` here would be a WMI query re-deriving what the
runtime already had.

The server **recomputes** the network and broadcast from the reported address
and prefix and discards the values the agent sent. A host that could claim a
network address it is not on would be a host that could join any link's relay
group it liked.

The server then groups candidate senders by that computed network. For a host
to be woken it looks for agents that:

1. computed the same network **and the same prefix** from their own
   interfaces — a /16 and a /24 with the same network address are not one
   link,
2. can actually broadcast there: the interface up *and running* (a configured
   NIC with the cable out sends nothing), the link carrying a broadcast
   address at all (a /31 point-to-point pair and a /32 host route do not),
   and **not wireless** — an access point will not bridge a broadcast to a
   station that is asleep and therefore not associated, so a wireless relay
   sends into a link the target has already left,
3. have checked in inside a freshness window (`AWAKE_WITHIN`, 900s — three
   poll intervals; the machine has to be awake to send anything, and a host
   that last polled yesterday is not a sender), and
4. are not the target itself.

Ties are broken by most recent check-in, and **more than one is asked**. A
magic packet is one UDP datagram to a broadcast address; sending three is
free, and the alternative is a wake that silently does nothing because the
one chosen sender went to sleep between the poll and the send.

A request is written to `agentWake`, one row per (target, sender), because a
wake ordered now cannot be relayed now — the neighboring agent finds out when
it next polls — and FOG had nowhere to write that down. Each row carries an
expiry (`TTL`, 600s), which is what keeps a wake from becoming a standing
instruction: a laptop that comes back next Tuesday must not be handed a wake
somebody ordered last week, by which time the machine is either already awake
or deliberately off.

### It is off until an install turns it on

`FOG_AGENT_WAKE_RELAY_ENABLED`, default `0`. This asks one customer machine to
put traffic on the network on behalf of another, which is a thing an estate
owner opts into rather than discovers after an upgrade.

### The fallback stays first

The server keeps doing what it does today — `wakeUp()`'s fan-out to every
node — and this is *additional*. An estate with a storage node on the link
never needs an agent to do it, and nothing about that path changes.

---

## 4. What the agent is told, and what it may do with it

A `wake` capability, gated on the existing `powermanagement` module, because
that is the module an admin already turns off to stop FOG touching a machine's
power. The block is a short list of pending wakes:

```json
"wake": {
  "targets": [
    {"id": 41, "macs": ["00:11:22:33:44:55", "00:11:22:33:44:56"]}
  ]
}
```

**There is no address in it.** The agent sends to its own interfaces'
broadcast addresses and to `255.255.255.255`, exactly as `WakeOnLan::send()`
does on a node. It cannot be pointed at a unicast address, at another
subnet's broadcast, or at anything else, because there is no field in which to
say so. That is deliberate: an agent that accepted a destination would be a
UDP reflector for whoever could feed it one, and the fact that only the server
can feed it one today is not a property worth relying on.

The constraints, each one a thing the agent enforces itself rather than
trusting the server to have got right:

| Rule | Why |
|---|---|
| Destination is always broadcast, port 9, never unicast | An agent that can be aimed is an amplifier |
| Payload is always exactly the 102-byte magic packet for the given MAC | Nothing else may be put on the wire |
| MACs are parsed and re-serialized by the agent, never passed through | A malformed MAC becomes a refusal, not a datagram |
| At most `MaxTargets` per poll, at most one packet per MAC per interface | A bounded amount of traffic per poll, whatever arrives |
| Nothing is sent when the block is absent | Absence is not an empty instruction |

`MaxTargets` is a constant in the agent, not a server-supplied number, for
the same reason the destination is not on the wire.

### The one that matters: "only other FOG clients"

Tom's constraint on this feature, and it is enforced in two independent
places rather than one:

- **On the server**, because it is the only side that knows: a target must be
  a row in `hosts`, and the MACs are that host's own `hostMAC` rows. There is
  no path from an arbitrary MAC to the block.
- **On the agent**, structurally: it has no way to be given a target other
  than the poll answer, which is authenticated with its client certificate
  against a server it pinned at enrollment (design 0002).

Neither of those alone is the guarantee. The server check is the real one;
the agent's is what stops a compromised or spoofed server from turning the
fleet into something worse than it already could.

---

## 5. What it reports, and the pending-MAC question

Each wake is reported like any other item result: `sent` with the number of
packets, or `failed` with the reason (no usable MAC, no broadcast address, the
socket was refused).

**The pending row is the authorization.** This is the only item report whose
id is another host's, and what makes that safe is that the server refuses any
result for which there is no pending `agentWake` row naming this sender and
that target — a 404. Without it any enrolled agent could write a result
against any host in the estate, and a wake could be reported twice.

The count is carried as its own field rather than left in the detail sentence,
because it is a number an admin sorts a report by — and because "sent" with a
count of zero is a lie the existing path has no way to catch. Caught exactly
that way: the first end-to-end run put four real magic packets on the wire and
recorded `packets=0`, because the agent had no field to put the number in.

The server records it against the wake request, so
"FOG asked three machines to wake host 41 and all three said they sent it"
is a thing an admin can read — which is more than the current path offers,
where a wake is fire-and-forget and a machine that stays asleep is
indistinguishable from a wake that never left the building.

On §1.4's unfiltered MAC list: the wake block excludes `hmPending` MACs, the
way `Group::wakeOnLAN()` already does and `Host::wakeOnLAN()` does not. A
pending MAC is one FOG has seen but nobody has accepted, and asking the fleet
to broadcast at it is exactly the wrong default. This is a **behavior
difference from the existing path**, deliberately, and it is confined to the
new one — `Host::wakeOnLAN()` is left alone, because narrowing it is a
separate change with its own blast radius.

---

## 6. Latency, and why it is acceptable

A wake ordered now is sent when the neighboring agent next polls, so up to
one poll interval — five minutes by default.

That is fine for the case this exists for. The overwhelming use of WoL in FOG
is a scheduled overnight imaging or update window: `TaskScheduler` fires the
wake, and the task the machine is being woken for is not going anywhere.
Nobody is standing at the machine.

The interactive case — an admin clicks Wake Up and watches — is the one that
would notice, and it is also the one where the admin is usually on the same
network and can see whether it worked. If it turns out to matter, the fix is
the existing poll-interval machinery (a shorter interval, or the agent's
existing wait-or-fire loop learning a wake window), not a new inbound channel.

---

## 7. What this is not

- **Not a wake for arbitrary addresses.** There is no field for one and no
  code path to one. If someone wants FOG to wake a device it does not manage,
  that is a different feature with a different security argument, and this
  design does not open the door to it.
- **Not a replacement for the node fan-out.** §3: the existing path runs
  first and unchanged. This adds links FOG could not previously reach.
- **Not a mesh.** Agents do not talk to each other, do not discover each
  other, and do not know each other exists. Every decision is the server's;
  the agent is a sender.
- **Not a replacement for the `wolbroadcast` plugin.** An estate that has
  directed broadcast working and a list of addresses maintained keeps it.
- **Not WoL over the internet.** A magic packet is a link-layer broadcast.
  Waking across a WAN is a different problem (a router that proxies ARP for
  a sleeping host, or a management controller), and out of scope.
