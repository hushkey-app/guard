# The cluster

An instance on the dashboard is derived from telemetry: it exists because
something posted to guard, and it disappears when that stops — which is exactly
the moment you most want to know about it. A **machine** is the opposite: it is
declared, watched from the outside, and can therefore be reported as down.

A machine is two addresses that answer two different questions, plus the path
and the cadence. Only the first is required.

| | | |
|---|---|---|
| **Address** | `https://vps-1.example.com`, or `http://localhost:8000` | where the *service* answers |
| **Health path** | `/api/health` | hangs off the address |
| **SSH** | `root@10.10.182.113` + a password | the *machine*, for the commands below |

The health path belongs to the address, not to the SSH host: a health endpoint
is part of a service, and the same `/api/health` should follow it from
`http://localhost:8000` today to a public domain tomorrow. The machine's own IP
is a way in, never a check target — a service behind a load balancer answers on
a name that belongs to no single box.

## Groups

A machine carries a **group** — "VPC-1", "staging", "the rack in the office" —
and `/cluster` lays itself out by it: one heading per group, with how many
machines are in it and how many are down, then the rows. Empty is normal and
lands under **Ungrouped**, which sorts last.

It is free text, and typed rather than chosen, because guard cannot know
whether the boundary that matters to somebody is a VPC, a region, a customer or
a floor — and a dropdown of the ones it could infer would be wrong for the first
person whose boundary is none of them. The groups already in use are offered as
suggestions, which is what stops "VPC-1" and "vpc-1" becoming two groups.

A group is not a tag. A tag is one of many labels for *finding* a machine again;
a group is where the machine *is*, so there is exactly one, and it is what the
page is arranged by. Duplicating a machine copies its group — five boxes in one
VPC is the case duplicate exists for.

## What a row shows

`/cluster` is a **list**, one line per machine, grouped. It was a grid of cards
and cards were the wrong shape: two to a line, mostly whitespace, and nothing
lined up — so finding the machine that was unlike the others meant reading all
of them. Twenty machines are now twenty lines you can run an eye down.

The head is everything needed to *find* a machine, in fixed columns:

| | |
|---|---|
| left | status dot, favicon, name, state badge, tags, address |
| middle | response time · when last checked, 24h uptime · how many checks |
| right | CPU, memory, disk as percentages, the check strip |

Those columns are the design. A percentage three rows apart is a comparison;
"7.7 GB / 29 GB" is a sentence you have to read. The full figures are one click
away and are the tooltip meanwhile, and a machine at ninety per cent of anything
turns that number red — the only colour on the row, because it is the reason
somebody opened the page.

The three machine figures are **absent** where guard has no login, rather than
dashed: the column holding open would say a gap means nothing, and here a gap
means something.

Opening a row shows what is needed to *act* on it — the meters with their bars,
the load and containers, **Running here** (the services whose telemetry says this
machine served them), the cloud strip, and the commands with their schedules and
**History**. Shut is the default and open is what is remembered, so a machine
added later lengthens the list by a line rather than by a screenful.

## The address is dialled from the server

Guard runs inside the network. The browser reading the dashboard is usually
outside it, on a laptop, on a VPN that routes some of it, or on a phone. So the
address may be `localhost:8000` or `10.0.0.4` and the check still works: guard
fetches it, and the row shows guard's answer.

The row only renders it as a link when a browser could plausibly follow it —
loopback and the private ranges are shown as text with "from the server" next to
them. An `<a>` pointing at `http://10.10.10.10:8000` from an HTTPS page fails
twice over, mixed content and then no route, and a control that always fails
teaches people to distrust the page rather than the network. The same reasoning
is why a machine's favicon is fetched by guard and stored, rather than linked
(`internal/cluster/prober.go`).

## A login is proved before it is stored

Give an SSH address and a password on the add form and guard connects before the
machine is saved; if it cannot get in, nothing is stored and the reason is on
the form. A machine saved with a mistyped password looks exactly like one saved
correctly, and the difference is otherwise discovered at 3am by somebody
pressing **Reboot**.

Leave both empty and none of that happens. A machine watched through a load
balancer, or one nobody has a login for, is a normal thing to have here — the
health check needs no credentials. The same check runs when the address or the
password is edited later; renaming, repathing or pausing a machine dials
nothing.

## Actions

An action is a name and a command: **Reboot** / `sudo reboot`, **Update** /
`apt-get update && apt-get upgrade -y`. Press the button, guard opens an SSH
session, runs the line, and shows the combined stdout and stderr with the exit
code and how long it took.

Guard does not know what any of it means. There is no allow-list of safe
commands, no parser, no library of blessed ones — those would all be guesses
about somebody else's machine. What guard offers is that the line you already
type at 3am is stored next to the machine you type it at, with a record of how
it went last time.

Three rules make the feature narrower than it looks:

- **The API takes an action id, never a command.** Anything that runs was
  stored first, which is the difference between an audit trail and a shell.
- **The machine comes from the action, not from the request.** A stored command
  cannot be aimed at a different box by the caller.
- **Every run is logged** through `slog`: node, user, address, command, exit
  code. The browser tab it was pressed in does not outlive that line.

The endpoints are `POST /api/cluster/run` (one action) and `POST
/api/cluster/ssh` (a connection test: `uname -sr; uptime`, which changes
nothing). Both are `admin`, like everything else that writes — and, as with
adding a machine, anyone holding that token could add a machine and run things
on it anyway.

Bounds: two minutes per command (`GUARD_SSH_TIMEOUT`), ten seconds to connect,
256 KB of output kept and the rest marked truncated.

## Schedules

An action can carry a **schedule**, and then guard runs it — the same stored
command, on the same machine, through the same login, with the same audit
line. The only thing a schedule changes is who pressed the button.

It is written as five cron fields **in UTC** (`0 */6 * * *`), as `@every 6h`,
or as `@daily`, `@hourly`, `@weekly`. UTC because a server's timezone is
invisible from this dashboard, and a backup landing at the wrong hour is how
you would otherwise find out what it was. Empty — which is the normal case —
means the command only runs when somebody presses it.

Five rules make this a scheduler rather than a job queue:

- **The five fields and the period mean different things.** A cron expression
  is a calendar: `0 */6 * * *` is midnight, six, noon, six, whatever happened
  in between. `@every 6h` is a period from the *last run*, so a dump that took
  forty minutes still waits six hours after it finished.
- **A schedule never overlaps itself.** If the last run is still going when
  the next is due, the new one is **skipped** and the skip is recorded as a
  run with that outcome. Two `pg_dump`s racing into one bucket is worse than a
  late backup, and a job that has outgrown its interval should be visible as a
  row of skips rather than as a backup that quietly halved its frequency.
- **A paused machine runs nothing.** The same switch that stops the health
  checks stops the schedules, because a box somebody is rebuilding is the last
  one that should have a backup job opening sessions into it.
- **The clock survives a restart.** The anchor is the last run, in SQLite —
  so a guard that was down over a window runs the missed job once when it comes
  back, rather than waiting out a fresh interval or firing for every window it
  missed. An action that has never run counts from when its schedule was
  written, which is why editing a command's *name* does not push its next dump
  six hours out.
- **A scheduled run gets longer than a pressed one.** Half an hour
  (`GUARD_SCHEDULE_TIMEOUT`) against two minutes, because the jobs people put
  on a timer are dumps and syncs, and a backup killed at two minutes is a
  backup that has never once worked.

## What ran, and what stopped running

Every run — scheduled or pressed — is a row in `cluster_runs`: when, how long,
the exit code, the outcome (`ok`, `failed`, `skipped`), who triggered it, and
the tail of its output. Fifty rows per command, read from **History** on the
card, and gone when the command is.

The other half is the **staleness alert**, which is the part worth being
careful about. An action can carry a budget — "alert if no success in 420
minutes" — and a separate loop checks it every five minutes
(`GUARD_ALERT_INTERVAL`).

Separate on purpose, and this is the whole design:

- **The check does not live inside the job.** A check that runs as part of the
  dump only ever fires on a day the dump ran, so the one failure it exists to
  catch — nothing ran at all — is the one it cannot see. This loop reads the
  database, so it still speaks when the scheduler is wedged, dead, or was never
  started.
- **It reads the last *success*, not the last run.** A job failing on the dot
  every six hours has a very recent last run, which is exactly what makes it
  easy to miss.
- **The threshold is not derived from the schedule.** A six-hourly dump that
  is six hours and one minute late is not news; one that has not worked since
  yesterday is. Only the person who knows what the job is for can say where
  that line is.
- **It survives a restart**, because the last success is a column rather than
  a variable — a redeploy that reset the clock would silence the alarm at
  precisely the moment it was due to fire.
- **Delivery does not travel over the thing being reported on.** The alert is
  always a line in guard's log, and additionally a JSON `POST` to the destination
  the command names (**Settings → Alerts**) — its own HTTP client, not the SSH
  runner whose jobs are failing. A command that names none is logged and nothing
  else. A stale job is reported once and then repeated
  every six hours (`GUARD_ALERT_REPEAT`) until it succeeds; a success clears
  the flag, so the next failure is announced afresh.
- **A delivery that failed is not a delivery.** Anything but a 2xx leaves the
  flag unset, so the next pass tries again — an alert swallowed by a 401 would
  otherwise be an outage nobody hears about twice.

### The webhook

One `POST`, `Content-Type: application/json`, carrying the alert's fields plus
a `text` beside them — so the same URL works for a chat hook that renders
`text` and for something that reads the fields:

```json
{
  "at": "2026-08-15T02:00:00Z",
  "node_id": 4, "node": "DB-1",
  "action_id": 7, "action": "Dump to R2",
  "schedule": "0 */6 * * *",
  "last_ok_at": "2026-08-14T17:00:00Z",
  "stale_for": "9h0m0s", "threshold": "7h0m0s",
  "message": "Dump to R2 on DB-1 last succeeded 9h0m0s ago (2026-08-14T17:00:00Z), past its 7h0m0s threshold",
  "text": "Dump to R2 on DB-1 last succeeded 9h0m0s ago …"
}
```

Some receivers need a credential and some do not — a Slack or Discord incoming
webhook has none, because the URL *is* the secret. A stored destination carries
the token for the ones that do:

| Receiver | The destination holds |
|---|---|
| Slack / Discord incoming webhook | the URL only |
| Your own endpoint / a paging service | `+ token: xxx` → `Authorization: Bearer xxx` |
| Something naming its own scheme | `token: "Bot xxx"` → sent as written |
| Something with its own header | `+ header: X-Api-Key` → the token verbatim, there |

A bare token becomes a Bearer credential; one that already contains a space is
sent unchanged, because `Bearer Bot xxx` is what nothing wants. With a custom
header the token is never rewritten.

Guard does not speak any messaging app's API and deliberately does not: the
thing between "a backup is late" and a phone is a relay somebody already has —
a Slack hook, an n8n flow, a small handler that reads `message` and forwards
it. What guard owes that relay is one authenticated `POST` with the facts in
it.

A budget with no schedule is a normal thing to set: a job run by CI or by a
person every morning is exactly as capable of quietly stopping.

## The password

The password is typed once and then never leaves the server again.

- **Encrypted at rest.** AES-256-GCM, a random nonce per record, in
  `internal/secrets`. The database file is a thing that gets copied to laptops,
  backed up to object storage and attached to bug reports; encrypting the
  column is what stops that file from being a list of logins. It does not — and
  is not meant to — stop somebody who already runs this process.
- **Shown as dots.** The API never sends the value back. It sends
  `has_password: true`, and the dashboard draws a fixed number of dots that says
  nothing about the length. The box is read-only until you press **Change**,
  which clears it: there is nothing to edit in place, because what is on screen
  is not the password. Changing it means typing the new one in full.
- **Written only when it changes.** The field is a JSON pointer, so absent means
  "leave it alone", `""` means "forget it", and a value means "this is the new
  one". A form that renames a machine sends no password and does not lose one.

The key comes from the first of these that exists:

1. `GUARD_SECRET_KEY` — a base64 or hex 32-byte key, or any passphrase.
2. A key file beside the database (`guard.db.key`), created on first use with
   mode 0600. This is what happens if you do nothing. **Back it up with the
   database, and do not commit it** — a stolen database file is useless without
   it, a stolen directory is not.
3. For an in-memory database, a key that exists only in the process.

Change the key and the stored passwords stop being readable — they are not
corrupt, they were sealed with something else. Type them again.

## The host key

The first successful connection pins the machine's host key; every connection
after it is refused unless the key matches. That is trust on first use — the
same bargain `ssh` itself makes, minus the prompt — and the fingerprint is shown
in the panel so it can be compared against what the machine's owner says it
should be.

Repointing a machine at a different SSH address clears the pin, because as far
as the host key is concerned that is a different machine.

## Locking a machine

**Lock** closes a machine for good. One click, a typed confirmation, and from
then on:

- the **SSH address** and the **password** cannot be changed;
- the **command list is closed** — nothing added, nothing edited, nothing
  removed, from the page or from the API;
- the commands that exist can still be **run**, which is the point of having
  locked it.

It cannot be undone from anywhere: not this page, not another tab, not a
handwritten request. Trying to clear the flag returns the machine still locked.
The only way past it is **deleting the machine**, and that is the design rather
than a gap — an undeletable row would be worse, and deleting is loud: it takes
the uptime history, the pinned host key and every saved command with it, and
what is left is a new row with no login and nothing pinned. A lock protects the
list you are looking at from quietly growing a line nobody noticed. That is the
move it exists to stop: an add is one request away from an arbitrary command
sitting in the list looking exactly as official as the ones somebody vetted.

Everything harmless stays editable — the name, the address, the health path, the
cadence, pausing. None of them can run anything.

It is enforced in the store rather than in a handler, so every writer goes
through it and the dashboard is not the thing being trusted. The page draws the
same rule — the add and save buttons disappear, the fields go read-only — because
a control that is going to be refused should not look like one that will work.

## How the machine is doing

The health path answers for the **service**, and on most boxes the service is a
container: `/api/health` says the app is fine while the host it sits on is out
of memory. A cloud provider does not close that gap either — Vultr's API
reports bandwidth and nothing about CPU, memory or disk. The only thing that
can answer is the machine, so guard asks it, over the SSH login it already
proved.

Once a minute (per machine, `0` turns it off), guard opens a session and runs
**one fixed, read-only command** that lives in guard's source — `/proc`, `df -Pk /`
and `docker ps`. It is never anything from the machine's command list, which is
why it is allowed on a locked machine: locking closes the list of things
somebody can add, and this is not on it.

The card then shows three bars — **CPU**, **memory**, **disk /** — the load
average, the host's uptime, an hour of CPU as a sparkline, and a chip per
container: green for up, red for stopped *or* `(unhealthy)`, because "Up 3 days
(unhealthy)" is not up in any sense worth colouring green.

Three things are deliberate:

- **Memory used is total minus *available*, not minus free.** A healthy Linux
  spends everything spare on cache and hands it back on demand; the free number
  is what makes people think a fine machine is about to fall over.
- **CPU is a rate, so the first sample has none.** `/proc/stat` counts since
  boot, and a percentage from one reading would be the machine's whole life
  averaged. A dash says "not measured yet"; `0%` would say "idle", which is a
  different and wrong answer. Gaps in the sparkline are drawn as gaps.
- **A failed sample is stored as one.** "Guard has not been able to get in
  since 04:12" is information; a card quietly showing the last good numbers
  would be lying by omission.

The cadence is per machine because a sample costs a fresh SSH handshake, where
a health check costs a request on a connection that was already open — a minute
against three seconds. **↻** on the card samples now instead of waiting. Samples
are kept for a day, like the checks.

Nothing is sampled unless a machine has a stored login, so this costs nothing
on a machine guard only watches from outside. If you want longer history or
finer detail than this, the other route is an OpenTelemetry Collector with the
`hostmetrics` receiver posting to guard as ordinary metrics — chartable in
`/views`, at the price of an agent per machine.

## The cloud account behind it

A machine can be linked to the instance it runs on, in a stored cloud account —
which adds the half a health check cannot see: whether the box is powered on at
all, what it costs, what it can be rolled back to. It is optional, and a machine
that is not in anybody's API is watched exactly the same way.

The rules are the ones above, applied to somebody else's API: the endpoints take
a node id and read the instance off the link, a locked machine refuses every
change at the provider, and every press is logged. Read
[docs/cloud.md](cloud.md) before changing any of it.

## Duplicating a machine

The copy button next to pause takes the address, the health path, the cadence
and the commands onto a new machine — the fleet case, where five boxes differ by
an address and otherwise want the same four commands. Typing that out five times
is how the fifth one ends up with a slightly different reboot command.

It copies the shape and not the identity. No login: a password proved against
one box proves nothing about another, and every stored login here is one that
connected at least once. No lock: that is a statement about a machine somebody
finished configuring, and this one is not. No run history: the copy has never
run anything. And it arrives **paused**, because until you change the
address it is pointing at the machine it was copied from.

## The machine's environment

Every machine has a box of `KEY=value` lines under **Settings → Cluster**, and two
buttons under it.

**Save** stores them in guard. Nothing happens on the machine — this is somebody's
intent, typed or pasted once, and a locked machine may still be edited here.

**Inject on machine** puts them on the box. One SSH command writes both places
Linux takes an environment from:

```
/etc/environment                               logins, PAM, anything shell
/etc/systemd/system.conf.d/10-guard-env.conf   every systemd service
```

Both, because "the machine's environment" is a different file depending on what is
reading it: a box whose apps are all systemd units gets nothing from
`/etc/environment` alone. A `systemctl daemon-reexec` follows so the manager
re-reads its defaults. **Services already running keep the environment they
started with** — restarting one is one of the machine's stored commands, and guard
says that after every inject rather than implying the change is live everywhere.

Containers are the exception worth knowing: docker does not pass the host's
environment in, so a containerised app still needs its `env_file` or `-e`. What
this gives that case is the file to point at.

### The paths are fixed, and that is the feature

There is nothing to declare and no path to fill in first. A list of files to
manage is what turns "put these variables on that box" into a chore, and every
extra option is a decision somebody has to make before they can do the thing they
came to do.

### What is safe about it

- **Guard writes what guard rendered.** The inject request carries a node id and
  nothing else: the variables come from the database, the paths are constants, and
  the login comes off the machine. There is no shape of that call that writes
  chosen content to a chosen place.
- **Values travel as base64.** An environment is full of passwords with quotes and
  dollars in them, and every bug in a thing like this is a quoting bug. Nothing a
  value contains reaches the command line.
- **Each file is replaced atomically, and the old one is kept** as `.guard-bak`
  beside it — written to a temporary name in the same directory and renamed over
  the target, so nothing ever reads half an environment, and putting it back is one
  `mv` for somebody who cannot reach guard.
- **The values are sealed at rest**, with the keeper the SSH passwords use. They
  are shown in the box in the clear, because a box of masked fields is not an
  editor, and every endpoint here is admin — including the read.
- **The lock refuses the inject, not the save.** Editing guard's copy changes
  nothing on the machine; writing to the machine is the thing a locked machine does
  not allow.
- **A value is written back the way it will be read.** The box is rendered by the
  same function that writes the file, so a value containing ` #` or a quote comes
  back quoted rather than truncated by the next save.

### Saved, and on the machine

The machine list carries a count and two dates — never the values, because that
list is fetched every three seconds. When what is stored is newer than what was
injected, the line goes amber and says so: **"saved since"** is the state this
pair of dates exists to make visible, and it is the difference between a variable
being in guard and being on the box.

## What is stored

`cluster_nodes` gains `domain`, `health_path`, `ssh_address`, `ssh_password`
(ciphertext, a BLOB), `ssh_fingerprint` and `locked`. (A `sealed` column exists
from a short-lived two-flag version and is folded into `locked` on migration;
nothing reads it.) The `url` column is still
the address that gets probed, but it is now derived — the store computes it from
`domain` + `health_path` on every save, so the prober and the topology's host
matching keep reading one column and cannot drift from what is configured.

`internal_url` is from an earlier shape of this feature, where the address was
split in two and the internal half won the probe. It is still read, so a machine
stored then keeps being checked where it was, and it is cleared the first time
that machine's address is saved. Nothing writes it.

`cluster_nodes` also carries `provider`, `provider_account_id` and
`provider_instance_id` — the cloud link, and nothing else about the instance,
which is read live. `cluster_snapshots` maps a snapshot id to the machine it was
taken of, because the provider's snapshot does not record where it came from.

`cluster_actions` is one row per command: name, command, position, and how the
last run ended. Existing machines are migrated by parsing their old single URL
back into a domain and a health path, so they open the new form already filled
in rather than looking unconfigured.

The unattended half is five more columns on the same row rather than a job
table, because what runs on a timer is the command that was already there:
`schedule`, `schedule_from_ns` (when the expression was written, which is what
an action that has never run counts from), `stale_after_s`, `last_ok_ns` and
`alerted_ns`. `cluster_runs` is the history — one row per run, fifty kept per
command, carrying the outcome, the trigger and the last 8 KB of output. It is
deleted with its command and with its machine.

`cluster_env` is one row per variable per machine — key, position, and the value
sealed with the keeper the SSH passwords use. `cluster_env_state` is the two dates
beside it: when the set was last saved, and when it was last written to the box.

## Confirmations

Locking, deleting and running all ask first, through one native `<dialog>` — so
Escape, the focus trap and the inert background are the browser's job rather
than three more listeners. Running shows the command in full, because "Reboot"
is a label somebody chose and the line underneath it is what actually happens.

Locking, and deleting a locked machine, ask you to **type the machine's name**.
A dialog with a button that says yes is a dialog people click without reading;
having to type the name is the difference between agreeing and noticing.
