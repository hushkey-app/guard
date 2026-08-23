# Analytics

Guard measures what the backend did. Analytics is the one signal that measures
what a *person* did — which page they landed on, and whether they pressed the
thing the page exists to make them press.

One sentence organises all of it: **the URL is the group, an action is a
column.** A path is a row, an action somebody decided mattered is a column, and
the cell where they meet is the number that made somebody build the page.

Three things to know before anything else, because each is otherwise learned
the hard way:

- **Guard counts sessions, not people.** Not users, not visitors. The word
  "sessions" is on the page everywhere it means sessions, and the other words
  are nowhere.
- **A rollup is not reversible.** Once `analytics_seen` has been purged, the
  session counts for those days stand: they cannot be recomputed, and a path
  rule written today cannot rewrite yesterday.
- **Relay through your own origin.** Every blocklist blocks a path called
  `analytics` or `track`. The direct URL works and is the thing that quietly
  stops working for a third of your visitors.

## How to use

The instructions live behind a **How to use** button rather than on the page:
four tabs — the tag, tracking actions, origins, and the relay — in
`components.HowTo`, a reusable dialog any page can fill with its own tabs.

They were inlined on `/analytics` before, which meant four paragraphs and two
notes above a page that has data on it a minute later: read once by whoever set
it up, in the way forever after. The button appears on the install card *and* on
the live page, because the question does not stop being asked once the first
beacon lands — the second one is "how do I track a button", and by then the
install card is gone.

The dialog opens and closes with a checkbox and a sibling selector, so it needs
no JavaScript and nothing to re-bind after a navigation. Only switching tabs is
scripted, delegated on `document` in `guard.js`: showing panel N from radio N
would need a `peer-checked/N:` class built from an index, and Tailwind emits no
class it cannot find written out.

## Turning it on

There is no `GUARD_ANALYTICS_*` variable, and there will not be one. Analytics
is the third path on the browser door, so it is on exactly when the door is:

```bash
GUARD_RUM_ORIGINS=https://app.example.com,https://www.example.com
```

Configured is on, unconfigured is off — the same rule signing in keeps. The
tracker, the beacon endpoint and `/v1/rum/traces` come up together, because they
are one door with one origin allowlist, one rate limiter and one body budget.

`/analytics` therefore has three states and shows exactly one, decided by
`GET /api/analytics/health` rather than guessed from an absence of rows:

| health says | the page is |
| --- | --- |
| `enabled: false` | Analytics is off, and it names the variable |
| `enabled: true`, no `last_event` | the Install band, alone |
| `enabled: true` and an event has arrived | the strip, the grid, the fold |

The middle one matters: a configured tracker nobody has visited yet draws the
same empty grid as a door that was never opened, and "analytics is off" is the
sentence that must never be shown to somebody whose tracker is working.

## Installing the tracker

One tag. No build step, nothing to `npm install`, no consent banner to fit.

```html
<script defer src="https://guard.example.com/v1/rum/track.js"></script>
```

`GET /v1/rum/track.js` is served by guard itself, embedded in the binary from
`internal/ingest/track.js`, cached for a day. It is not CORS-gated — a script
tag never is — so the script is public and the *door* is what is guarded. The
URL carries no version on purpose: a version in it is a number every customer's
page has to be edited to change, and the cache header is then also the ceiling
on how long a fix takes to reach a browser that already has one.

The tracker posts to `/v1/rum/events` on the origin it was served from, unless
told otherwise:

```html
<script defer src="https://app.example.com/track.js"
        data-endpoint="/api/telemetry/events"></script>
```

That is the relay shape, and it is the recommended one — see
`docs/browser-telemetry.md` for the handler and for the `X-Forwarded-For` line
that keeps guard's rate limit counting visitors rather than counting your relay.
Serve the script from your own origin too if you can; a blocked script is a
blocked script whatever it posts to.

What the two kilobytes do:

- fire `page_view` on load and on every `pushState`, `replaceState` and
  `popstate`, so a single-page app needs no second call
- expose `guard.track(name, props)` — a name and a flat bag of strings
- bind **one** delegated `click` listener, in the capture phase so an app that
  stops propagation still counts, and fire the action named by the nearest
  `[data-guard-track]` ancestor
- batch, flushing on a five-second timer, at fifty events, on a navigation, and
  on `visibilitychange: hidden` through `navigator.sendBeacon` — the only flush
  that survives a tab closing
- write nothing but a session id, to `sessionStorage`
- do nothing at all when `navigator.doNotTrack === "1"` (still answering
  `guard.track`, because a tracker that throws breaks the site it measures)
- stop for good after two 4xx answers in a row, because retrying a
  misconfiguration from every visitor's browser is how it becomes a load test

**The selector lives in your markup**, which is the whole of guard's answer to
PostHog's autocapture:

```html
<button data-guard-track="signup_click">Start free</button>
```

Guard will never store a CSS selector. A selector in guard's database is a copy
of somebody else's markup that guard cannot keep correct, and it reads zero on
the day the markup changes — which is guard's cardinal sin, since an empty
window here is silence rather than zero.

## Sessions, not people

A session id is 16 random bytes in `sessionStorage`, minted on the first event
and expired after 30 minutes of inactivity. **No cookie, no IP hash, no
fingerprint** — which is what lets the tracker run without asking anybody's
permission first.

That is a statable weakness rather than an oversight: one person in two tabs is
two sessions, and tomorrow they are a new session. The alternative — a daily
rotated salted hash of address and user agent — means storing something derived
from an address that guard's browser door discards on principle, and buys a
number that is still an estimate. A metric that is exactly what it says beats
one that is approximately what somebody wanted.

The id is also the **join key**. `guard.session()` is published for the
OpenTelemetry web SDK sitting beside the tracker to tag its spans with:

```js
provider.register();
const tracer = trace.getTracer("app");
const span = tracer.startSpan("checkout", {
  attributes: { "rum.session_id": window.guard.session() },
});
```

Guard indexes `rum.session_id` (`indexedAttributes` in
`internal/telemetry/views.go`), so a path with a bad rate on the grid is one
click from the spans those visits produced. See **The walk into /traces**.

## The beacon, and what the door refuses

`POST /v1/rum/events` takes guard's own JSON rather than OTLP. The OTLP JS
exporter plus a web tracer is tens of kilobytes for a payload that is a name and
a path, and the tracker has a two-kilobyte budget — so the keys are one letter,
because this crosses somebody else's visitors' networks.

```json
{
  "s": "6f2a9c1d4e8b7a30",
  "p": "/pricing",
  "u": {"s": "google", "m": "cpc", "c": "spring"},
  "r": "news.ycombinator.com",
  "e": [
    {"n": "page_view", "t": 1755900000123},
    {"n": "signup_click", "t": 1755900004411, "d": {"plan": "team"}}
  ]
}
```

Everything a browser sends is from a stranger, so the limits are the edge of
what guard will store, checked before anything is written — and they are
**refusals rather than clamps**, because a beacon quietly truncated is a tracker
that looks like it is working and is not:

| limit | value |
| --- | --- |
| events in one beacon | 50 |
| action name | 64 characters of `[a-z0-9_.-]` |
| props on one event | 8, flat, strings only |
| a prop value, a campaign field, a referrer | 200 characters |
| a path, after normalisation | 200 characters |
| body | 256 KB, shared with the rest of the door |
| requests | 120 a minute per address, shared with the rest of the door |

One bad event refuses **the whole beacon**, not the offending line: a batch that
arrives half-stored is a count nobody can reason about. The tracker knows this
and drops a name that fails its own check rather than letting a typo take the
page views batched beside it.

A refused beacon is counted for the health page and nowhere else. It is the one
refusal worth surfacing: an unknown origin is somebody else's site and a rate
limit is a flood, but a malformed beacon looks like nothing at all — the page it
came from is fine, and the grid is quietly missing what it sent.

There is no `Content-Type` check, because `navigator.sendBeacon` posts a string
as `text/plain` — which is what keeps the flush from a closing tab free of a
preflight it would never live long enough to complete.

## What is stored

Six tables, and one rule: **the rollup is the truth, the raw feed is a
courtesy.**

| table | what it is | kept |
| --- | --- | --- |
| `analytics_events` | the raw feed, one row per event, the path uncollapsed | `retention_hours`, with the rest of the telemetry |
| `analytics_rollup` | `(day, path, action)` → events, sessions | `analytics_rollup_days`, default 90 |
| `analytics_seen` | `(day, path, action, session)` — what makes the counts exact | `analytics_seen_days`, default 7 |
| `analytics_sources` | `(day, path, campaign, referrer host)` → sessions | with the rollup |
| `analytics_actions` | the discovered names, and which are columns | **configuration** — in the backup |
| `analytics_path_rules` | the ordered rules, first match wins | **configuration** — in the backup |

The rollup exists because analytics that vanish at the event cap are analytics
nobody trusts: the question is always "versus last month", and one row per day,
path and action is thousands of rows a month rather than millions. The raw feed
is there to be read the afternoon somebody is instrumenting a page, and it is
what a path rule is previewed against: it keeps the normalised URL with no rule
applied to it, which is exactly what somebody needs in order to write the rule.

**How the session counts stay exact without a sketch.** On every event:

```sql
INSERT OR IGNORE INTO analytics_seen(day, path, action, session) VALUES(?,?,?,?);
-- the write that changed nothing is the write that says "already, today"
INSERT INTO analytics_rollup(day, path, action, events, sessions) VALUES(?,?,?,1,?)
  ON CONFLICT(day, path, action) DO UPDATE
  SET events = events + 1, sessions = sessions + ?;
```

No HyperLogLog, no approximation to explain on a dashboard. The price is
`analytics_seen`, the one table here that grows with traffic rather than with
content — so it is purged first, and **after that a day's session counts cannot
be recomputed**. That is the honest half of the trade, and it is why the seen
window is a number somebody can type.

One transaction per beacon, not per event: a batch is what the tracker actually
sends, and fifty commits would be fifty trips through the single writer
everything else in guard is queued behind.

**The day is guard's clock**, and it is a whole UTC day. An event's own
timestamp is kept on the raw row because it is what orders a session, but it
comes from a stranger's machine — a rollup keyed on it would let one visitor
with a wrong laptop write a day into next year.

## The ceilings

Three, all constants beside the code that enforces them rather than numbers on a
settings page, because each has one right answer: a table that grows until
SQLite is slow is not a table anybody chose.

| what | ceiling | past it |
| --- | --- | --- |
| distinct paths in a day | 1,000 | counted under `(other)` |
| distinct action names, ever | 200 | **refused**, and counted on the health page |
| distinct campaigns per path per day | 100 | counted under `(other)` |

They are answered differently on purpose. A path or a campaign past the ceiling
is still counted, in a row whose figure is real and whose name says what
happened. A **name** past it is refused whole, raw row included: two teams'
events silently folded into one column is a wrong number nobody can see is
wrong, where a missing one is a number on the health page. `page_view` is exempt
— it is the Views column, and a cap that could silence it would cap the page
itself.

The path ceiling closes the door on new paths rather than moving ones already
through it, so a page does not stop having numbers halfway through the afternoon
the flood started.

## Path rules

`/users/*` → `/users/:id`. Ordered, first match wins, and a second rule for a
pattern that already has one is refused while somebody is still typing it — a
row that sits on the page looking like configuration and can never fire is worse
than a refusal.

A pattern is a **glob, not a regular expression**, because the thing being
written is a URL shape. `*` stops at a separator, which is what makes that rule
mean "a user" rather than "everything under `/users`"; a second level is a
second `*`. The replacement is a literal — a replacement that could carry the id
back would be a way to write a rule that collapses nothing.

**Rules are applied at ingest**, so a rule shapes what is stored rather than
what is drawn, and they are applied *before* the path ceiling — collapsing
`/users/*` is what stops a day's thousand paths ever being reached. The trade is
said out loud on the page: changing a rule cannot rewrite the days already
rolled up. The alternative, applying rules at read, would mean every read
re-deciding what a path is, and a rollup keyed on something that changes
underneath it is a rollup whose rows nobody can add up.

The editor previews against the last 100 distinct paths the tracker actually
sent, from the raw feed. The preview runs the same preparation and the same
application the save runs — the rule the `.env` import keeps — so the dialog
cannot describe something other than what happens.

## Actions: discovery, and the one decision

Names are **discovered**, never declared: a tool that wants registration before
it records is a tool people abandon halfway through instrumenting. Everything
that arrives is counted.

Pinning is the person's half, and it is what makes a name a **column**, in a
stored order. Unpinned actions still count and still appear when a path row is
opened — that is the discovery half, and the pin button in the fold is how a
column gets created. `page_view` is reserved: always counted, never a column,
because it *is* the Views column.

Pinning, unpinning and reordering are one request carrying the whole ordered
list, because they are one decision — and two writes are two chances to leave an
order nobody chose. A name that has never been seen is refused: a column
nothing was ever counted in reads as a zero.

**Deleting an action is a purge, not a mute.** It takes the rollup rows, the
seen rows and the raw rows with it, and the dialog says so. What it cannot do is
un-discover the name — the next beacon carrying it discovers it again, because
discovery is what the tracker does rather than something guard was told.

## The grid, and the rate

One row per path, ordered by views, in fixed columns — the same reason
`/registries` and `/storage` are lists: alignment down the page is what makes
the row that is unlike the others findable. Sortable by any column.

Each pinned action's cell carries a count and a **rate**, and the rate is the
only reason the column exists: the sessions that did the action over the
sessions that saw the page. Both halves are summed the same way, over the same
window, so the ratio is between two numbers of the same kind.

**A dash, never a zero**, where an action was never seen on a path. `0.0%` next
to a column that page has no button for is a lie in a fixed-width font. A path
with actions and no page views keeps its counts and gets no rate at all — that
is a real thing (a tracker firing on a route the page view never reached), and
inventing 0% for it would be a conversion figure guard made up.

Sessions are summed per day, which is the unit the rollup is keyed in: somebody
who came back on Tuesday is Tuesday's session too. A denominator that shrank the
longer the window somebody picked would be worse.

A row opens onto what a row has no width for: the sparkline of views and
sessions, **every** action on that path with its pin button, the top campaigns
and referring hosts into it, and the link into `/traces`. Shut is the default;
open is what is remembered — the rule `/cluster` keeps.

## The strip

Sessions, page views, views per session and actions per session, each against
the window of equal length immediately before it. The previous window is
computed rather than asked for twice, because two windows that do not line up
are a change figure nobody can check.

The window is bounded at both ends and **there is no "all retained"** — a real
answer everywhere else in the dashboard. The rollup is keyed by whole UTC days
and the second half of the strip is the window before this one; an unbounded
window has no previous, so it is refused rather than compared against nothing.
A request naming no window gets seven days.

One honest limit, worth knowing before it is noticed: **Sessions comes from
`analytics_seen`**, because that is the only table that can tell one visit to
three pages from three visits to one — summing the rollup's per-path sessions
would make views per session read 1.0 forever. So on the defaults the strip can
count sessions seven days back while Views is complete for ninety, and a longer
window draws the ratios as nothing rather than as a number. Raise
`analytics_seen_days` toward the rollup window if the strip is the thing you
read, and know that you are growing the table that grows with traffic.

## Retention

Two numbers on **Settings → Data storage**, beside the two that were already
there, applied when saved rather than at the next start:

| setting | default | what it bounds |
| --- | --- | --- |
| `analytics_rollup_days` | 90 | the rollup and the sources — "versus last month" |
| `analytics_seen_days` | 7 | exact session counts, and how far the `/traces` walk reaches |

The raw feed is not a third number: it goes with the rest of the telemetry on
`retention_hours`, because "how long is raw telemetry kept" already has one
answer. Seen may not be kept longer than the rollup — it exists to make the
rollup's counts exact, and a day of it beyond the rows it explains is the table
that grows with traffic growing for nothing. The ceiling on either is ten years,
which is a refusal rather than a recommendation.

These are rows in `settings`, not environment variables: the `GUARD_*`
catalogue is twenty-two and stays twenty-two.

## The walk into /traces

The link off an opened path row is the whole reason the session id is a join
key. It carries the **path** and the window, never the session ids:

```
/traces?rum_path=/pricing&range=7d
```

The ids stay in the database. `model.Filter` turns the path into
`attr_rum_session IN (SELECT session FROM analytics_seen WHERE path = ?)`, so
the link is a URL somebody can read in a status bar, paste into a message and
still open next week — and the set it names moves as the window does.

Two things follow from that. The walk reaches exactly as far back as
`analytics_seen` is kept, which is days where the spans themselves are kept in
hours — so in practice it is the spans that run out first. And it only finds
spans that were tagged: the browser SDK has to put `guard.session()` on them, or
there is nothing on the far side of the join.

The window travels with the link and is widened, not narrowed: `/traces` has no
30-day option, so anything longer than a week arrives as "all retained" — the
widest true answer, rather than a silently narrower one that would read as
"nothing happened".

## Health

`GET /api/analytics/health` is the answer to "is the tracker working", and most
of it is what guard threw away: beacons refused at the door, names refused past
the discovery cap, paths and campaigns rolled into `(other)`, the number of
names known, the size of `analytics_seen`, and when the last event arrived by
guard's clock.

The counters are the process's, since it started, and they are **counts rather
than rates or a status**. A tracker being silently dropped is the failure mode
people take weeks to notice; somebody reading the page decides what a hundred
rejections means, and no range control can make the number look better.

`last_event` is read from the raw feed, so a tracker that stopped longer ago than
`retention_hours` says nothing at all rather than a date. Nothing is the honest
answer: what guard has is no event in the window it keeps.

## Backup

`analytics_actions` and `analytics_path_rules` travel — they say how guard is
configured. `analytics_events`, `analytics_rollup`, `analytics_seen` and
`analytics_sources` do not, and are named in `backupExcluded` beside them, for
the same reason logs are not: they are the part that grows, and they say nothing
about configuration. A restored guard has its columns and its rules, and starts
counting.

## The endpoints

| Method | Path | Role | What |
| --- | --- | --- | --- |
| `GET` | `/api/analytics` | — | the strip and the grid over one window, in one answer |
| `GET` | `/api/analytics/path` | — | one opened row: its days, and where its sessions came from |
| `GET` | `/api/analytics/actions` | — | every discovered name, pinned first |
| `POST` | `/api/analytics/actions` | admin | the pinned names, in the order they are drawn |
| `DELETE` | `/api/analytics/{id}` | admin | an action and everything counted under it — the id is the name |
| `GET` | `/api/analytics/rules` | — | the path rules, in the order they are applied |
| `POST` | `/api/analytics/rules` | admin | the whole ordered list, replacing what was there |
| `POST` | `/api/analytics/preview` | — | what those rules would make of these paths, storing nothing |
| `GET` | `/api/analytics/health` | — | what is being dropped, and whether the door is open |

The strip and the grid are one request because they are one window: two calls a
second apart are two windows, and the numbers somebody is reading side by side
would not add up. The fold asks a second time, and it is the one place that is
allowed to — the grid is a thousand paths at the ceiling and a window is ninety
days, so carrying every series in the grid's answer would put ninety thousand
points on the wire, on a timer, to draw the one row somebody opened.

## What this is not

Not yet, and each is a whole product: session replay, funnels, cohorts and
retention curves, identified users, revenue, attribution modelling, A/B tests,
heatmaps. Analytics is also not a source the view compiler can read, so an
analytics figure cannot carry a `internal/viewalerts` rule the way a saved view
can — the numbers are on the page, and nothing is watching them for you.

Permanently not: stored CSS selectors, and autocapture of every click.
