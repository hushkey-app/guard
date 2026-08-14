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
internal/remote/ssh.go        running one stored command on one machine
internal/secrets/secrets.go   AES-GCM at rest, for the SSH passwords
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
- The panels are cloned from `<template>` elements declared in `client/ui/components`, so
  panel chrome stays real shadcn markup and every class stays where Tailwind looks. The
  package doc explains why.

## The cluster

A machine is declared rather than discovered, so guard can say "VPS-1 has been
down for six minutes" about a box that stopped talking to anyone. Two pages
share one renderer (`client/public/cluster.js`): `/settings/cluster` declares,
edits and locks machines; `/cluster` is the operational surface — cards with
each machine's stored commands ready to run, and deliberately nothing that
could redefine one. A card's **command list** folds away (the count stays on
the header, the state and the address never move); what a card always shows
is its status and its **tags**, which is what a wall of them is scanned for.
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
make            # generate + build WASM and server
make dev        # watch, rebuild, restart, reload the browser — use this, not `go run .`
make css        # rebuild client/public/app.css (needs Node; only after new classes)
./guard         # OTLP/HTTP + dashboard on :4318
go test ./...
```

`make dev` proxies :4318 in front of the restarting binary, so exporters posting
telemetry keep their connection target and a failed build keeps the last good binary
serving with the compiler error shown in the dashboard.

`GUARD_DB_PATH`, `GUARD_RETENTION_HOURS`, `GUARD_MAX_EVENTS`, `GUARD_TOKEN` configure it;
flags of the same names override. `GUARD_SECRET_KEY` is the key the SSH passwords are
sealed with — unset, guard generates `<db>.key` beside the database, which is part of the
backup and never part of the repository. `GUARD_SSH_TIMEOUT` bounds one command run.

Sign-in adds `GUARD_GOOGLE_CLIENT_ID`/`GUARD_GOOGLE_CLIENT_SECRET`, the four
`GUARD_APPLE_*` values (`CLIENT_ID` is the Services ID; the key is
`GUARD_APPLE_PRIVATE_KEY` or `GUARD_APPLE_PRIVATE_KEY_FILE`), `GUARD_ADMIN_EMAIL`
(the addresses that are always admins), `GUARD_AUTH_BASE_URL` (pin it behind a proxy —
the redirect URI is compared as a string at both providers) and `GUARD_AUTH_SESSION_TTL`.
All optional; set none and nobody is asked to sign in.

## Local dependency

`go.mod` has `replace github.com/mirairoad/howl-go => ../howl-go` while the framework
changes are unreleased. **`docker build` will fail with this in place** — the Dockerfile
only copies guard's own source. Remove the replace and pin a published version before
building an image.

## Logging

`console.Setup` installs the tinted slog handler; the request logger tags `ip`/`ua` for
callers that are not one of guard's own pages — i.e. the exporters posting telemetry, which
is usually what you want to see. Log through `slog`, not `fmt.Printf`.
