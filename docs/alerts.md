# Alerts

Guard tells the outside world one way: **one POST of JSON to a URL you name**,
with a credential if that URL wants one. It speaks no messaging app's API and
deliberately does not — between "the disk is full" and a phone sits a relay
you already have (a Slack hook, an n8n flow, a handler that reads `message`),
and what guard owes that relay is the facts, not an SDK that breaks when Slack
moves.

Three things raise events, and all three go through the same module
(`internal/notify`): a scheduled command that has stopped succeeding, a rule
about a machine, and — next — a saved view. One delivery path means one place
to fix a token format, and one list of destinations to point everything at.

## Destinations

A destination is a name, a URL and an optional token. Named, because "so I can
direct where I want" is the whole point: the backups go to the channel the
person who owns the database watches, the page-me rules go somewhere that wakes
somebody up.

| Receiver | What to set |
|---|---|
| Slack / Discord incoming webhook | URL only — the URL *is* the secret |
| Your own endpoint, a paging service | URL + token → `Authorization: Bearer <token>` |
| Something naming its own scheme | token `Bot xxx` → sent as written |
| Something with its own header | header `X-Api-Key` → token verbatim, there |

The token is sealed with AES-GCM like an SSH password, never comes back
(`has_token`, and the page draws dots), and changing it means typing the new one
in full. **Test** sends a real event of kind `test`, so what you are checking is
the shape that will arrive at 4am, and records the answer — a token typed with a
trailing space looks exactly like a quiet week otherwise.

Deleting a destination deletes the rules pointing at it. A rule with nowhere to
send is worse than no rule: it evaluates, decides something is wrong, and tells
no one, while the page still lists it.

## Rules

A rule is: which machine, which measurement, which way, how far, for how long,
and where to say so.

Every measurement is one the cluster page already shows — the health check, its
latency, the day's uptime share, and what the machine says about its own CPU,
memory, disk, host uptime and stopped containers. A rule adds a comparison and a
POST, never a second source of truth: if the number is on the card, a rule can
watch it, and if it is not, no rule can invent it.

Four rules about the rules:

- **A condition has to hold.** "CPU above 90% *for five minutes*" is a monitor;
  "CPU above 90%" is a sampling artefact, because one sample during a log
  rotation is not an incident. The hold survives a restart — it is a column,
  not a variable, so a guard that restarts hourly can still reach five minutes.
- **Recovery is its own event.** A rule that fired sends `state: resolved` when
  it stops, so a receiver can close its own incident rather than inferring it
  from silence.
- **A machine that cannot answer is silent, never zero.** CPU, memory, disk and
  containers come from the machine over SSH; a box with no stored login has no
  reading at all, and a rule reading that as 0% would be a rule that never
  fires. A **paused** machine is not "down" either: pausing is deliberate, and
  it is the last thing that should page somebody.
- **A rule with no machine covers every machine**, including the ones added next
  month. "Any disk over 90%" is one rule, not one per box.

A firing rule repeats every `GUARD_ALERT_REPEAT` (6h) until it clears. Editing a
rule forgets where it stood — a threshold that just moved from 90 to 95 is a
different question, and the old "already told them" flag would answer it with
the old one's silence.

## Panels that watch themselves

A saved view can carry a rule, edited in the **Alert** section at the bottom of
the builder drawer — beside the query rather than on a rules page, because the
query above *is* the rule's question and describing it twice is how a chart and
its alert end up disagreeing.

The rule reads **the latest value the panel would draw**: the last bucket of a
time series, the value of a stat, the largest category. Where the query splits
into series, the **worst** one decides — the highest for an "above" rule, the
lowest for a "below" one — and the event names it. Averaging across series
would be the number that hides exactly the outage worth alerting on.

Three consequences worth knowing:

- **An empty window is not a zero.** A "below" rule over a window with no rows
  is silent, because telemetry pausing for a minute would otherwise page
  somebody every time — and a rule people turn off catches nothing.
- **A query that will not run is not an alert.** A field renamed out from under
  a saved view is guard's problem; it gets a log line, not a page.
- **Four shapes can be watched** — time series, categorical, single and
  scatter. A scatter is one dot per event, so its reading is the **worst dot in
  the window**: "the slowest request in the last fifteen minutes was 9.4s" is
  the sentence that panel exists to answer. A waterfall or a heatmap has no one
  number a person reads off it, and the builder says so rather than offering a
  threshold that never fires.
- **A threshold is typed in the panel's own unit.** A latency rule is entered in
  milliseconds because that is what the chart is drawn in — a threshold in
  different units from the chart is a rule nobody can check by looking — and the
  form says what it means: `5000` shows `= 5s` underneath.

Editing a rule drops its "already told them" stamp but *keeps* whether it is
firing, so the next pass either re-fires against the new line or sends the
resolved event. The alternative — forgetting everything — closes an incident
silently at the far end, and leaves somebody's on-call board with a row that
never clears. The machine rules behave the same way.

## The payload

```json
{
  "at": "2026-08-15T02:18:38Z",
  "kind": "cluster.rule",
  "subject": "DB-1/disk_percent",
  "state": "firing",
  "title": "Disk / above 90%",
  "message": "DB-1: Disk / is 96%, above 90% for 5m0s",
  "text": "DB-1: Disk / is 96%, above 90% for 5m0s",
  "fields": {
    "node_id": 1, "node": "DB-1", "url": "http://10.19.96.4/health",
    "status": "up", "metric": "disk_percent", "value": 96.4, "unit": "%",
    "op": "above", "threshold": 90, "for_seconds": 300, "held_seconds": 312,
    "latency_ms": 78, "uptime_percent": 100, "checks": 11890,
    "cpu_percent": 2, "mem_percent": 23.4, "disk_percent": 96.4
  }
}
```

`kind` and `subject` are for routing, `message`/`text` for a human, `fields` for
anything that filters or draws. `text` duplicates `message` under the key a chat
webhook renders, so one URL serves both a Slack hook and a handler that parses
properly. A metric a machine cannot answer is `null` rather than `0`.

The whole card travels with every event on purpose: an alert that says "disk
96%" and makes somebody open the dashboard to learn the box is also out of
memory has cost a trip it did not need to.

A delivery that is refused — anything but a 2xx — is **not** a delivery: the
"already told them" flag stays unset and the next pass tries again, so an outage
cannot be swallowed by a 401.

## Environment

`GUARD_ALERT_WEBHOOK`, `GUARD_ALERT_TOKEN` and `GUARD_ALERT_HEADER` are still
read, and are the destination used by a scheduled job that names no stored one —
an instance configured before there were named destinations does not go quiet on
upgrade. `GUARD_MONITOR_INTERVAL` (30s) is how often the machine rules are
evaluated, `GUARD_VIEW_ALERT_INTERVAL` (1m) how often the watched views are run
— slower, because each pass is somebody's compiled query against the same table
the dashboard is reading — `GUARD_ALERT_INTERVAL` (5m) how often the staleness budgets are, and
`GUARD_ALERT_REPEAT` (6h) how long anything firing stays quiet between
repeats.
