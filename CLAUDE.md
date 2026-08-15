# Guard

An OTLP/HTTP telemetry receiver with a server-rendered dashboard. Logs, traces and metrics
land over OTLP, get stored in SQLite, and are explored from a howl-go UI.

```
main.go                       wiring: store, howl-go app, ingest routes
internal/ingest/otlp.go       OTLP/HTTP + the JSON API the dashboard reads
internal/telemetry/model.go   storage, retention, snapshots
internal/telemetry/cluster.go the machines watched from outside, and their commands
internal/telemetry/views.go   saved views: the query compiler and its CRUD
internal/telemetry/provider.go the stored cloud accounts and their sealed keys
internal/cloud/               the provider vocabulary: neutral types, the halves a provider may implement
internal/vultr/               one Vultr account: registries, compute, object storage
internal/cloudflare/          one Cloudflare account: R2 buckets
internal/s3/                  the object half: SigV4, list a prefix, sign a download
internal/cluster/prober.go    the health polling — outbound HTTP
internal/cluster/stats.go     the host sampling — one fixed read-only command over SSH
internal/cluster/scheduler.go the stored commands that carry a schedule
internal/cluster/watchdog.go  "no successful run in too long" — its own loop, its own delivery
internal/cluster/monitors.go  the rules over what the cluster page measures
internal/notify/              one POST of JSON to a named destination — every watcher's way out
internal/viewalerts/          the saved views that carry a rule, run on a timer
internal/remote/ssh.go        running one stored command on one machine
internal/secrets/secrets.go   AES-GCM at rest, for the SSH passwords and the stored secrets
internal/telemetry/vault.go   the secrets: environments, values, the keys that read them
internal/vault/               the second binary's half: read the file, answer a key
cmd/vault/                    guard-vault — secrets served while guard is down
internal/auth/               sign in with Google or Apple, and who is allowed in
client/pages/                 the dashboard — howl-go filesystem routes
client/ui/ui.templ            shared page furniture (nav, stats, filter bar, pagination)
client/ui/components/         panel chrome, panel bodies, the view builder
client/public/core.js         helpers shared by the three client modules
client/public/charts.js       the panel renderers, hand-drawn SVG
client/public/views.js        the views page: panels, builder, live preview
client/public/guard.js        tables, filters, detail panel, the live tick
client/public/registries.js   the registries drill
client/public/cloud.js        the cloud accounts, the machine link, the provider strip
client/public/storage.js      the object storage page
client/public/secrets.js      the secrets page: environments, pairs, keys, .env import
client/styles/app.css         stylesheet source — compiles to client/public/app.css
```

## The framework

Guard is built on **howl-go**. Its conventions are not guessable — Go's toolchain rejects
`_layout.templ` and `[id].templ`, so the routing, page and shell rules look different from
any JS framework.

**Read `llms.txt` from the howl-go module before touching anything under `client/pages/`.**
It is at `../howl-go/llms.txt` in this working copy (see the `replace` directive in
`go.mod`), and served at `/llms.txt` by the howl-go docs site.

`.mcp.json` in this repo wires up the framework's MCP server: `howl_conventions`,
`howl_check`, `howl_routes`, `howl_endpoints`, `howl_scaffold`. Call `howl_check` after
editing; run it from the shell with
`go run github.com/mirairoad/howl-go/core/cmd/howl check`.

The short version:

- `client/pages/**/index.templ` → routes; `*.client.templ` also renders through
  `client/public/views.wasm`; `app.templ` is the document shell; `layout.templ`
  wraps its directory. All reserved names.
- `fsroutes_gen.go`, `*_templ.go`, `views.wasm`, and `wasm_exec.js` are
  **generated** — `make` runs routes, templ, WASM, then the server build.
- Pages import `core/router` (never `core/app`), take no arguments, and read everything
  from `ctx`.
- Middleware is plain `func(http.Handler) http.Handler`.
- The JSON API lives in `server/apis/`, one file per endpoint, generated into
  `apis_gen.go` and a typed client at `client/api/`. OTLP stays raw in `internal/ingest`.

## The UI

The dashboard is **shadcn-templ v2** (`github.com/axadrn/shadcn-templ/v2`), imported
directly as Go packages. Read `docs/shadcn-templ.md` before changing it — it is the
offline digest of the exact beta pinned here; do not guess React shadcn APIs or fetch
the website first.

Two things are load-bearing and easy to break:

- **`<html class="dark style-nova">`.** `style-nova.css` nests every `cn-*` rule under
  `.style-nova`, so dropping that class silently unstyles every component.
- **`client/public/app.css` is a build artifact but is committed.** Tailwind only emits
  the classes it finds in the `@source` globs (pages, `client/ui`, and each JavaScript
  file by name — see the `css` target). Use a class nothing used before and you must run
  `make css`, which is the one target that needs Node. **A new file under
  `client/public/*.js` needs its own `@source` line**, and a class assembled from a
  variable (`` `col-span-${n}` ``) is never emitted at all — those go in inline styles.

Interactive shadcn components (select, dialog, popover) need the upstream JS bundle,
which the direct-import workflow does not serve. Guard uses static components only;
filter controls are native elements wearing `cn-native-select` / the Input component,
because `guard.js` drives them through `.value`.

## Views

A view is a stored query — filters, a grouping, an aggregation, a window — and running one
produces a `model.Frame`, which is a *result layout* rather than a chart. Panels declare
which layout they read (`model.Panels`), the compiler promises one (`model.ShapeOf`), and
the pair is checked in `ViewQuery.ValidateFor`. That seam is the whole design: adding a
panel that reads an existing shape is a renderer in `charts.js` and an entry in
`model.Panels` — no SQL, no endpoint, no migration.

- `internal/telemetry/views.go` compiles a query per shape. **Nothing there interpolates
  caller text into SQL** — a field is looked up in `model.Columns` or bound as a JSON path,
  and values are always parameters. Keep it that way; it is the only reason a dashboard
  user may compose queries at all.
- Grouping by an attribute is a `json_extract` over every candidate row. Five attributes
  (`http.route`, method, status, `client.address`, `db.system`) have generated columns and
  indexes, and both OpenTelemetry spellings of each resolve to the same column on purpose.
  Adding a sixth means an entry in `indexedAttributes` and nothing else.
- Percentiles are exact nearest-rank, computed with window functions, because SQLite has
  no percentile function and a silently interpolated p95 is a number people act on.
- **A duration is drawn as a duration.** `model.AlertUnit` marks a query measuring
  `duration_ms`, the frame carries `unit: "ms"`, and `withUnit` in `core.js` turns
  that into `9.4s` / `2m 30s` everywhere — axis, tooltip, stat, gauge. "15,000 ms"
  is a number somebody converts in their head before it means anything.
- The panels are cloned from `<template>` elements declared in `client/ui/components`, so
  panel chrome stays real shadcn markup and every class stays where Tailwind looks. The
  package doc explains why.

## The cluster

A machine is declared rather than discovered, so guard can say "VPS-1 has been
down for six minutes" about a box that stopped talking to anyone. Two pages
share one renderer (`client/public/cluster.js`): `/settings/cluster` declares,
edits and locks machines; `/cluster` is the operational surface — a **list**, one line per
machine, grouped, with the stored commands a click away and deliberately
nothing that could redefine one. The head is what a machine is *found* by —
status, name, tags, address, and five numbers in fixed columns — and the fold
is what it is *acted on* by. Shut is the default; open is what is remembered.
A tag is a label
plus one of ten named colours (`model.TagColours`, mapped to pixels once in
`cluster.js`), stored as JSON on the node and written on every save — so any
caller that updates a machine has to send them back or lose them. It carries an
**address** (where the service answers), a **health path** that hangs off it,
and optionally an **SSH login** plus the commands somebody keeps for it — read
`docs/cluster.md` before changing any of it.

The three rules that are load-bearing:

- **The health path hangs off the address, never off the SSH host.** The
  address is a service (`http://localhost:8000`, later a domain); the SSH host
  is a machine. Guard dials the address from the server, so it may be one only
  the server can resolve — the row links it only when a browser could follow it.
- **A login is proved before it is stored.** Give SSH credentials and the add
  fails unless guard can connect; give none and the machine is watched anyway.
- **The password never comes back.** It is sealed with AES-GCM
  (`internal/secrets`, key from `GUARD_SECRET_KEY` or a 0600 file beside the
  database), the API answers `has_password`, and the dashboard draws dots.
  Changing it means typing the new one in full.
- **What a machine says about itself** comes from `internal/cluster/stats.go`:
  once a minute per machine, one **fixed read-only command** in guard's source
  (`/proc`, `df`, `docker ps`) over the same SSH login, parsed into CPU, memory,
  disk, load, uptime and containers. Never a stored action, which is why it is
  allowed on a locked machine. The health path answers for the service — often a
  container — and a provider API answers for the power switch; neither knows the
  disk is full. Memory is total minus *available*; CPU is a rate, so the first
  sample has none and says so rather than showing 0%.
- **`POST /api/cluster/run` takes an action id, never a command**, and reads the
  machine off the action. Everything that runs was stored first, and every run
  is logged. A machine can also be **locked**: one way, confirmed by typing its
  name, after which the login is frozen and the command list is closed — no
  adding, editing or removing, from the page or the API. The only way past it is
  deleting the machine. Enforced in the store, not in a handler.
- **A command can carry a schedule**, and then guard runs it —
  `internal/cluster/scheduler.go`, a fourth loop the same shape as the prober
  and the collector. Five cron fields in UTC or `@every 6h`, one column on
  `cluster_actions`, no job table: what runs on a timer is the command that was
  already there, through the same login and the same audit line. A run that is
  still going is **skipped** rather than started twice, and the skip is a row.
  A scheduled run gets half an hour (`GUARD_SCHEDULE_TIMEOUT`) against the two
  minutes a pressed one gets, because the jobs people schedule are dumps.
- **The staleness watch is a separate loop** (`internal/cluster/watchdog.go`),
  and that is the point of it: a check that ran inside the dump would never
  fire on the day the dump did not. It reads `last_ok_ns` from the database —
  the last *success*, not the last run — so it still speaks when the scheduler
  is wedged, and it delivers through its own HTTP client
  (`GUARD_ALERT_WEBHOOK`) rather than the machinery it is reporting on. Every
  run lands in `cluster_runs`, fifty per command, read from **History** on the
  card. Read `docs/cluster.md` before changing any of it.

## Alerts

Everything guard tells the outside world leaves through `internal/notify`: one
POST of JSON to a **named destination** (`webhooks` table, token sealed like an
SSH password, `GUARD_ALERT_WEBHOOK` still honoured as the unnamed fallback).
Guard speaks no messaging app's API on purpose — read `docs/alerts.md` before
changing any of it.

- **A rule watches what the cluster page already shows** — health, latency,
  uptime share, and the machine's own CPU, memory, disk, host uptime and stopped
  containers (`model.MonitorMetrics`, read by `Node.Measure`). Adding a metric is
  a line in that list and a case in that switch; a rule can never invent a number
  the card does not have.
- **A condition has to hold, and recovery is its own event.** `for_seconds` is
  stored, so the hold survives a restart, and a rule that fired sends
  `state: resolved` when it stops so a receiver can close its incident.
- **Unmeasurable is silent, never zero.** No SSH login means no CPU reading, and
  a rule reading that as 0% is a rule that never fires. A paused machine is not
  down.
- **A rule with no machine covers every machine**, including ones added later.
- **A saved view can carry one too** (`internal/viewalerts`, the Alert section
  in the builder drawer): it reads the latest value the panel draws — worst
  series wins — and only the three shapes that have a single reading are
  offered. An empty window is silence, not a zero.
- **Editing a rule keeps "firing" and drops "already told them"**, so the next
  pass re-fires or resolves. Forgetting both would close an incident silently at
  the far end.
- **A refused delivery is not a delivery**: the alerted flag stays unset and the
  next pass retries, so a 401 cannot swallow an outage.

## The dashboard's data path

`client/public/store.js` is the store, and it lives **outside the outlet**: the
pages come and go through howl's client-side navigation, and this module is
evaluated once and stays. So the data belongs to the session rather than to a
page — navigating back is instant because the value is already in memory, and
two pages reading the machine list read the same list.

- `ensure(key, load, render)` draws what is known, then confirms it. `render`
  is told which of the two it is drawing, so a page can say "from your last
  visit" rather than quietly showing minute-old numbers.
- `set(key, value)` publishes, so a save that answers with the new row corrects
  every page holding it without a round trip. `subscribe` is the read side.
- Mirrored to `sessionStorage` for **cold** loads only — a reload or a new tab,
  where the module is evaluated fresh. It dies with the tab: a fleet that
  outlived its session would be a page confidently drawing yesterday.
- Nothing decides anything from it. Every read is followed by a revalidation,
  and a chart's numbers are never drawn from it — the views page caches panel
  *chrome* (titles, widths, order), never a frame.
- **A background refresh that changes nothing redraws nothing.** The store
  compares answers structurally and remembers what is on screen, so a repeat
  pass keeps the DOM — and a half-typed threshold, a text selection, a scroll
  position — intact. `guardPageUnmount` calls `screenCleared()`, because the
  outlet has just thrown the DOM away and the next page starts empty.
- Every page goes through it: cluster, views, storage, registries, cloud
  accounts, members, alerts. A page that fetched on its own would be the one
  that still says "Loading…" on the way back. `client/public/store_test.mjs`
  asserts the eight rules; `make test` runs it.

## Secrets

Guard stores what your applications boot with — key and value, per environment,
sealed with the same keeper the SSH passwords use — and a **second binary**
serves it. `internal/vault` + `cmd/vault` build `guard-vault`: no pages, no
ingest, no cluster loops, no import of any of them. Read `docs/secrets.md`
before changing any of it.

- **Two levels: a workspace is an application, its environments are its own.**
  pack, hushkey, auth — each with local, develop, staging and production,
  seeded when it is made. Flat naming (`hushkey-production`) is a convention
  doing a schema's job, and the first person to type it differently breaks it.
  A key scopes to one environment, which implies its workspace, and the token
  carries both names so a leaked one is actionable.
- **Guard writes, the vault reads.** They share a database file and a key file
  and nothing else, and they are deployed and restarted separately — an
  application asking for its database password at boot must not be waiting on
  the dashboard's release. That is a property of the build rather than a hope:
  the vault's store has no method that changes a secret, so no handler above it
  can grow one.
- **The value comes back, and that is the one rule here that differs.** An SSH
  password is a credential guard uses on somebody's behalf and is never read
  out; a secret is a value an application is going to be handed, and a store you
  can only write to just means the real copy lives in a file on a laptop. The
  page masks values until asked, because the person changing one should not have
  to expose forty.
- **The workspace and the environment come from the key, never from the
  request.** No `?env=`, no `?workspace=`, and there never can be: a leaked
  staging token cannot be pointed at production, and no application's key can
  read another's. One key, one environment — which is what makes revoking one
  mean something.
- **A token is opaque, not signed.** 32 random bytes, stored as SHA-256, looked
  up every fetch — the same bargain guard's browser sessions make. Revocation is
  therefore instant, there is no signing key to distribute, and no claim can
  disagree with the database. `GUARD_TOKEN` and a session cookie open neither
  door here; a test pins that, because today it is true by omission.
- **Unknown, revoked and expired are one answer**, and **bookkeeping never fails
  a fetch**: last-used and the capped read log are written on a throttle, and a
  failed write still serves the secrets.
- **Import reports before it writes.** `model.ParseEnv` takes the dialect people
  actually paste — `export`, comments, quotes, and a double-quoted value running
  over several lines, which is how a PEM key ends up in a `.env` — and the same
  call runs with `dry_run` for the confirm and without it for the write, so the
  dialog cannot describe something other than what happens. Skipped lines are
  named with their line numbers.
- **It will not invent a key.** No `GUARD_SECRET_KEY` and no `<db>.key` is a
  startup error, because a vault that generated one would come up healthy and
  hand out values it cannot decrypt.

## The cloud account

Guard stores exactly one secret per cloud account — the provider API key — and
that one key answers for three pages: `/registries`, the cloud strip on
`/cluster`, and `/storage`. It is one credential at the provider, so it is typed
once, proved once and revoked once. Read `docs/cloud.md` before changing any of
it.

There are two providers, **Vultr** and **Cloudflare**, and they do not answer
for the same things. `internal/cloud` owns the vocabulary — neutral types, plus
the verbs split into halves a provider may implement (`Registries`,
`RegistryMaker`, `Storages`, `StorageRenamer`, `StorageKeys`) — and each
provider package implements what its API actually has. **What a provider can do
is derived from what it implements, never declared beside it**: `cloud.Describe`
asks the interface questions, `GET /api/cloud/providers` answers them, and the
dashboard draws itself from that. So a Cloudflare account gets no power switch
and no Reveal button, and the endpoints refuse in words if asked anyway. Adding
a third provider is a package, one line of wiring in `server/apis/cloud`, and
one id in `model.Providers` — no new endpoints and no page edits.

Cloudflare needs a second, non-secret value: the account id every one of its
endpoints hangs off, stored in the clear as `provider_accounts.external_id` and
carried beside the key in `cloud.Credentials`. It has R2 and, for now, no
registries — its managed registry needs a Workers Paid plan, so the half is
simply not implemented.

The rules mirror the SSH passwords:

- **A key is proved before it is stored** — it has to answer the provider once —
  and it never comes back: the API answers `has_key`, the page draws dots.
  Rotation is delete-and-add, so the proof cannot be skipped. Removing an
  account unlinks every machine that pointed into it.
- **Nothing the key unlocks is stored.** Registries, tags, instances, snapshot
  state and storage subscriptions are read live on the page that shows them.
  The exceptions are associations guard made and nothing else can answer: a
  machine's `provider_instance_id`, and `cluster_snapshots`.
- **Every provider request leaves from the server**, and the credentials the
  provider hands back — a registry's docker login, an object storage's S3
  secret — never reach a neutral type: no listing struct in `internal/cloud` has
  a field one could land in, and the only way out is `cloud.StorageKeys`, whose
  two methods are reached from one `admin` endpoint each, both of which log.
- **A machine's provider endpoints take a node id, never an instance id.** The
  instance is read from the link through `Store.ProviderTargetFor`, which also
  applies the lock — so a locked machine reads and refuses to change, and no
  handler can forget to ask.
- **Deleting a tag deletes the manifest behind it** — the registry API has no
  other delete — so every tag sharing the digest goes with it. The dashboard
  says so before confirming; repository deletes require typing the name.
- **A registry can be created and cancelled** where the provider implements
  `RegistryMaker`: name, region and plan come from the provider's live price
  list, creating bills from the moment it is accepted, and deleting takes every
  repository in it, so it asks for the name to be typed and logs either way.
- **Browsing a bucket is read-only, by construction.** Objects are S3 rather
  than any provider's API (`internal/s3`, SigV4 from the server), and
  `cloud.StorageObjects` has three verbs — list buckets, list a prefix, sign a
  link — and none that changes anything, so no endpoint can. Downloads are
  signed links that expire in five minutes, minted on a press and logged; the
  bytes never pass through guard. Vultr needs no stored S3 pair (its API hands
  one back per subscription); R2 does, sealed and optional, and an account
  without one simply cannot open a bucket. That pair is the **only** part of an
  account edited in place (`PUT /api/cloud/accounts/s3`, proved before storing)
  — everything else is delete-and-add, and deleting an account unlinks every
  machine that points into it.
- Provider reads are fetched on mount and explicit refresh only, throttled in
  `registries.js`, `cloud.js` and `storage.js`: behind them is somebody's API
  rate limit, not guard's SQLite.
- **`/registries` and `/storage` are lists, like `/cluster`.** One line per
  registry or bucket, grouped under the account key it came from, with the
  figures in fixed columns so they line up down the page — that alignment is
  the reason a list beats a wall of cards for finding the one that is unlike
  the others. A registry row opens its repositories; a storage row folds open,
  because the credentials and the five buttons are not what somebody scanning
  the page came to read.

## Signing in

Guard can be put behind Google or Apple — read `docs/auth.md` before touching
`internal/auth/`. Three rules carry the whole design:

- **Configured is on, unconfigured is off.** No OAuth credentials and guard is
  the open tool it has always been, because the usual instance is a container on
  a laptop. Google credentials draw the Google button, Apple's draw Apple's,
  both draw two — the login page renders the providers that could be *built*, so
  a button that cannot work is never on screen. Half a configuration is fatal at
  startup: somebody who set a client id and forgot the secret meant to close the
  door.
- **The provider says who you are; the members list says whether you may come
  in.** OAuth will happily prove a stranger owns their own Google account. The
  allowlist is `auth_members` plus whatever `GUARD_ADMIN_EMAIL` names — checked
  beside the table rather than seeded into it, so it is always an admin, cannot
  be edited from the page, and is the way back in when the last stored admin
  removes themselves. A member reads; an admin also changes things, which is
  what the `Roles: []string{"admin"}` every write endpoint already declared
  starts meaning. The member is read **per request**, so a removal takes effect
  on the next one.
- **The session is guard's, not the provider's.** One id token, verified
  (signature against the published key set, issuer, audience, expiry, and the
  nonce this sign-in generated), then guard's own cookie. No access token, no
  refresh token, and the database stores a SHA-256 of the cookie rather than the
  cookie. The sign-in state lives in SQLite and is good for exactly one use,
  because Apple's callback is a cross-site form POST and a `Lax` cookie is not
  sent on one.

`/v1/…` stays outside all of it. An exporter holds a bearer token and cannot
sign in with Google, so a login screen there would break every collector pointed
at guard on the day it was switched on. `GUARD_TOKEN` still works everywhere and
still means "a machine, let it through". `/login` is a `.raw` route — its own
document, no shell — because the shell is the sidebar, and a login screen that
renders the navigation it is refusing is a login screen that leaks it.

## Browser telemetry

`/v1/rum/traces` and `/v1/rum/logs` are a second, narrower door for telemetry
from a browser — read `docs/browser-telemetry.md` before touching it. The rule
that matters: **the service identity is assigned by guard, never accepted from
the payload.** A browser holds no secret, so anything posted there is from a
stranger, and without that rule a visitor can write spans claiming to be your
API. Off unless `GUARD_RUM_ORIGINS` names an origin.

The recommended deployment relays through the application you already expose
rather than publishing guard: a server-to-server post carries no `Origin`,
which the intake allows for exactly that reason.

## Build and run

```bash
make            # generate + build WASM, server and guard-vault
make dev        # watch, rebuild, restart, reload the browser — use this, not `go run .`
make css        # rebuild client/public/app.css (needs Node; only after new classes)
./guard         # OTLP/HTTP + dashboard on :4318
./guard-vault   # the secrets server on :4319, reading the same database
go test ./...
```

`make dev` proxies :4318 in front of the restarting binary, so exporters posting
telemetry keep their connection target and a failed build keeps the last good binary
serving with the compiler error shown in the dashboard. It also starts
`guard-vault` on :4319 under its own watcher (`dev/vault`), sharing the
terminal — its lines carry `app=vault`, a failed build there leaves the running
vault alone, and `GUARD_VAULT_ADDR=` leaves it out. Two processes in develop
because the promise is that secrets keep being served while guard restarts, and
a setup where they only run when somebody remembered to start them never
exercises it.

`GUARD_DB_PATH`, `GUARD_RETENTION_HOURS`, `GUARD_MAX_EVENTS`, `GUARD_TOKEN` configure it;
flags of the same names override. `GUARD_SECRET_KEY` is the key the SSH passwords and the stored
secrets are sealed with — unset, guard generates `<db>.key` beside the database, which is
part of the backup and never part of the repository. **`guard-vault` will not generate
one**: without the key it refuses to start rather than answering with values it cannot
decrypt. `GUARD_VAULT_ADDR` (:4319) and `GUARD_VAULT_TOUCH` (1m, how often one key's use
is recorded) configure it. `GUARD_SSH_TIMEOUT` bounds one command run,
`GUARD_SCHEDULE_TIMEOUT` one scheduled run (30m). `GUARD_ALERT_WEBHOOK` is where a
staleness alert is POSTed — unset, it is logged and nothing else — with
`GUARD_ALERT_TOKEN` sent as `Authorization: Bearer` unless it names its own scheme,
or in `GUARD_ALERT_HEADER` verbatim when the receiver wants its own header.
`GUARD_ALERT_INTERVAL` (5m) is how often the budgets are checked,
`GUARD_MONITOR_INTERVAL` (30s) how often the machine rules are, and
`GUARD_ALERT_REPEAT` (6h) how long anything firing stays quiet between repeats.

Sign-in adds `GUARD_GOOGLE_CLIENT_ID`/`GUARD_GOOGLE_CLIENT_SECRET`, the four
`GUARD_APPLE_*` values (`CLIENT_ID` is the Services ID; the key is
`GUARD_APPLE_PRIVATE_KEY` or `GUARD_APPLE_PRIVATE_KEY_FILE`), `GUARD_ADMIN_EMAIL`
(the addresses that are always admins), `GUARD_AUTH_BASE_URL` (pin it behind a proxy —
the redirect URI is compared as a string at both providers) and `GUARD_AUTH_SESSION_TTL`.
All optional; set none and nobody is asked to sign in.

## Deploying

Two static binaries, no runtime dependencies, and the box pulls rather than
being pushed to — read `docs/deploy.md`. A tag builds `guard` and `guard-vault`
for linux/amd64 and linux/arm64 in GitHub Actions and publishes them with a
`SHA256SUMS`; `deploy/guard-update` on a systemd timer verifies, installs and
restarts. `/etc/guard/version` pins a box (`latest`, or a tag).

- **The updater is a shell script**, because it has to work on the day the
  binaries do not: no build step, nothing to update itself, curl and systemctl.
- **Guard first, then the vault, and always the same version.** Guard owns the
  schema and migrates on start.
- **Each service is health-gated and rolls back on its own.** The previous
  binary is kept beside the new one, so going back is a rename; a running binary
  cannot be written over, so installing is a rename too.
- `-version` on both binaries is what the updater asks — never a state file
  that can drift from what is actually installed.
- **The sidebar's Update card** (`internal/release`, `ui.UpdateCard`) polls the
  releases API server-side every 15m and writes `/etc/guard/version` when
  pressed — it installs nothing, which is why the card says "requested" rather
  than claiming to be finished. Guard writes only a version it has actually
  seen from the API, because that file is read by something running as root
  that puts the value in a URL. No `/etc/guard` means a link and no button.

## Local dependency

`go.mod` has `replace github.com/mirairoad/howl-go => ../howl-go` while the framework
changes are unreleased. **`docker build` will fail with this in place** — the Dockerfile
only copies guard's own source. Remove the replace and pin a published version before
building an image.

## Logging

`console.Setup` installs the tinted slog handler; the request logger tags `ip`/`ua` for
callers that are not one of guard's own pages — i.e. the exporters posting telemetry, which
is usually what you want to see. Log through `slog`, not `fmt.Printf`.
