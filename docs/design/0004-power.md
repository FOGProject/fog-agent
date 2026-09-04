# 0004: Power management through the coordinator

Status: built and proven 2026-09-03 (section 4). Follows the power row in
[0001](0001-architecture.md) section 7: "keep the server model; execution
goes through the coordinator".

## 1. What FOG has

A `powerManagement` row per host is either a schedule (five cron fields
plus `shutdown`, `reboot` or `wol`) or an on-demand action (`pmOndemand=1`,
no cron: an admin's "shutdown now" from the host list). Groups grant
schedules too (ADR 0038), resolved per host by `Assign\Resolver`. The legacy
client fetched `{onDemand, tasks[{cron, action}]}` from `Client\PM`, which
deleted the on-demand rows as it read them, and ran the schedules with its
own cron. Wake-on-LAN never reached the client: the server or a storage node
sends the packet, from `TaskScheduler` for due `wol` rows and from the host
list for an immediate wake.

None of that changes. This design is only about how the agent gets the
same two things and what it does with them.

## 2. Shape

Capability `power`, module `powermanagement`. The desired state carries

```json
"power": {
  "schedules": [{"cron": "30 22 * * 1-5", "action": "shutdown"}],
  "ondemand":  [{"id": 41, "action": "reboot"}]
}
```

**Schedules are desired state.** The agent keeps the resolved list and
fires it with its own five-field cron matcher (`internal/provider/power`),
sleeping until the earlier of the next poll and the next firing. A firing
is a forced reason to the reboot coordinator, mode `shutdown` or `reboot`,
so the grace countdown for logged-in users applies exactly as for a task.
The minute last fired is persisted, so a machine that comes back within the
same minute does not fire it twice.

**On-demand is a task that rides the desired state.** An admin's click
inserts the row, the revision moves, the agent fetches the state on its
next poll and hands each on-demand action to the coordinator as a forced
reason. It reports `power applied: on-demand shutdown accepted (id 41)`
at once, and that report is what consumes the rows on the server. The
legacy client consumed them on read; the agent consumes them on
acknowledgement, so a fetch that never reached the agent leaves the request
standing.

**Wake-on-LAN stays where it is.** `wol` schedules are dropped from the
block the way `Client\PM` dropped them; the server keeps sending the packet.

## 3. Decisions

| # | Decision | Why |
|---|---|---|
| 1 | Schedules fire in the agent, not the server | A shutdown must happen on time whether or not the poll lands on the minute; the legacy client did the same |
| 2 | Both kinds are forced reasons | A scheduled or clicked shutdown is the admin's decision; the coordinator still gives logged-in users the grace countdown |
| 3 | A mix of shutdown and reboot reasons reboots | The coordinator's existing rule: a reboot satisfies a shutdown's power cycle, a shutdown strands a task that needed the machine back. The legacy client preferred shutdown; the difference only shows when both are pending at once |
| 4 | On-demand rows are consumed on the agent's acknowledgement | One audit line says the agent has it, and a request the agent never received is not silently lost |

## 4. Proof

Linux lab VM (`fog-agent-test`, host 239), 2026-09-03:

- A reboot schedule set six minutes out fired on its minute
  (`reboot applied: power: scheduled reboot (0 23 * * *)`), the coordinator
  rebooted the machine, the agent came back polling, and `power_fired`
  persisted as that minute. A three-minute lead was too short: the row
  reached the agent one poll too late and it correctly did not fire for a
  minute already past.
- An on-demand shutdown row was acknowledged on the next poll
  (`power applied: on-demand shutdown accepted (id 2)`), the row was gone
  from `powerManagement` on that report, the coordinator shut the VM down
  (`reboot applied: power: on-demand shutdown`, VirtualBox state
  `poweroff`).
- Flipping the schedule row to `wol` left the agent's persisted schedule
  list empty on its next poll.

Windows VM (`telliottwin11`, host 105), same day, after the legacy
fog-client was removed from it: an on-demand shutdown was acknowledged
(`power applied: on-demand shutdown accepted (id 4)`), the row consumed,
and the coordinator shut the machine down after the 60 s warning for its
two logged-in users. Two things learned on the way:

- With the legacy client still installed, its power module read and
  deleted the row first and the agent never saw it. A host running both
  consumes on-demand rows with whichever asks first, which is why the
  installer should remove the legacy client (open slice).
- The agent had been upgraded in place from a build without `power`, and
  the inherited revision was already marked applied, so the row sat for
  ten minutes until an unrelated task moved the revision. The applied
  revision now also records the capability set that applied it, and a
  build with a different set converges the revision on its first poll.

Lab note: a reboot kills a `systemd-run` transient unit, so the agent on the
VM runs from a real unit file (`fog-agent-lab.service`).
