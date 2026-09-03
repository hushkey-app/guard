# Secrets

Guard stores the environment variables your applications boot with — one
workspace per application, its own environments inside it, key and value
encrypted at rest — and a **second binary**, `guard-vault`, hands them out.

Two processes on one database file is the whole design. Guard is a dashboard: it
gets deployed, it gets a bad release, somebody restarts it mid-edit. The thing
an application asks for its database password at boot must not be that. So the
dashboard writes and the vault reads, they share a SQLite file and a key file
and nothing else, and they are deployed, restarted and rolled back separately.

```
guard          /secrets, the API, the whole dashboard    :4318   may go down
guard-vault    GET /v1/secrets, bearer key, nothing else :4319   stays up
```

## What is stored

| table | what |
|---|---|
| `secret_workspaces` | one row per application in the VPC — pack, hushkey, auth |
| `secret_envs` | that application's stages: local, develop, staging, production |
| `secrets` | one key, one sealed value, per environment |
| `secret_keys` | the tokens applications hold: a SHA-256 and a visible prefix |
| `secret_reads` | who fetched what, capped at fifty per key |

Two levels, and only two. A **workspace** is an application; its **environments**
are its own stages. That is the whole hierarchy — no folders, no inheritance
between stages, no sharing a value across applications.

Two levels rather than one because a flat list is fine for one application and
unreadable for eight: `hushkey-production` beside `auth-production` beside
`pack-production` is a naming convention doing a schema's job, and the first
person to type `hushkey_production` breaks it. Environments belong to a
workspace rather than being global, so `hushkey` can have a `preview` that
`auth` does not, and two applications both having a `production` is
unremarkable rather than a collision.

A new workspace arrives with local, develop, staging and production already in
it, because adding an application should be one press. A fresh installation has
one workspace called `default`, and so does an installation upgrading from
before workspaces existed — its environments are moved there rather than
guessed at, and renaming it is a press.

Values are sealed with the same keeper the SSH passwords use — AES-256-GCM,
key from `GUARD_SECRET_KEY` or the `0600` file beside the database. **Back that
file up with the database.** Without it the rows are noise.

## The one rule that is different

Every other secret in guard is write-only. An SSH password goes in, the API
answers `has_password`, and the dashboard draws dots — because that password is
a credential *guard* uses on somebody's behalf, and nothing is served by handing
it back.

A secret is the opposite: it is a value an application is going to be given, and
somebody has to be able to read what production is actually configured with. A
store that could only be written to would just mean the real copy lives in a
file on a laptop, which is the situation this replaces. So values come back —
to an admin on the page, and to a key over the vault.

The page still masks them. Not because reading is forbidden, but because a
screen full of live credentials is a screenshot, a shoulder and a screen share
waiting to happen: the person who came to change one value should not have to
expose forty. One press shows one; **Show values** shows the page.

## Importing

Paste a whole `.env` file, as many times as you like. The parser takes the
dialect people actually have:

- `export` prefixes, `#` comments, blank lines
- bare, `'single'` and `"double"` quoted values
- `KEY=a=b=c` — only the first `=` separates
- escapes (`\n`, `\t`, `\\`, `\"`) inside double quotes, nothing inside single
- **a double-quoted value that runs over several lines**, which is how a PEM
  private key ends up in one of these and is the case a line-at-a-time parser
  silently truncates

Import reports before it writes. The page calls it once with `dry_run` — *"12
new, 3 changed, 41 already the same"*, plus every line it could not read **with
its line number**, because "3 lines skipped" sends somebody back to a
hundred-line file to work out which three — and then calls it again without.
Same function both times: a confirm dialog that describes something other than
what happens is the failure mode worth engineering out.

Existing keys are overwritten. Unmentioned keys are left alone unless **Delete
keys the file does not mention** is ticked, because the common paste is a
handful of new values and a default that quietly emptied an environment would be
the last time anybody used this.

**Copy .env** is the way back out, formatted on the server so what you copy is
byte-for-byte what an import reads back — one escaping rule, in one place, with
a test that round-trips it.

## Comparing, and copying across

Two presses on the same table, because "does production have what staging has"
and "give production what staging has" are one question asked twice.

**Compare** puts the workspace's environments side by side, read-only: a row
per key, a column per environment, up to eight of them — the point where a row
stops being readable at a glance, which is the whole reason to draw it as a
table. Every cell arrives with a state already decided by the server:

| colour | means |
|---|---|
| green | every environment here has this key with the same value |
| amber | they disagree |
| red | this one does not have it at all, or cannot decrypt what it has |

The state is about the **row**, not the value, so a key two environments share
and a third lacks is green, green, red — the pair really does match, and
saying otherwise sends somebody looking for a difference that is not there. A
value that will not decrypt is never called a match: it is not equal to
anything, least of all to an empty box beside it.

**The comparison works with every value still masked**, which is why the states
are computed on the server rather than by diffing strings in the browser. Three
colours answer "is production configured like staging" on a screen that is
being shared. *Show values* is there for the moment the difference itself is
what is needed, and it is off every time the dialog opens.

**Duplicate** is the same table over exactly two environments, with one press
per row:

- **→** copies that value into the environment on the right.
- **×** deletes it from there, and appears exactly when there is nothing left
  to copy — the two already agree, or the left-hand one does not have it.

So the arrow turning into a cross is the row saying it is done. *Copy every
difference* does the arrows in one press, after saying how many are new and how
many replace a different value; it never deletes, so a key production has and
staging does not is left alone.

Copying is `PUT /api/secrets/values`, once per key — **the same call the row's
own Save button makes**. There is deliberately no bulk-write endpoint: a second
way to write a secret is a second thing to get wrong, and this one is exercised
every day. After any press the whole comparison is read again, so what is on
screen is the database rather than an assumption about it.

Both modes stay **inside one workspace**. Two applications' environments hold
unrelated keys, so a table comparing `hushkey/production` against
`auth/production` would be a page of red boxes that means nothing.

`GET /api/secrets/compare?envs=7,8,6` is the one endpoint behind both, `admin`
like everything else here. The order of the ids is the order of the columns,
because "staging then production" and "production then staging" are two
different tables and whoever asked meant one of them.

## The keys

One key reads one environment. That is what makes revocation mean something: a
token spanning three environments is a token nobody dares rotate when one
service is redeployed, and "which of the seven services is still using it" is
not a question a hash can answer.

A token looks like `gsk_hushkey_production_R-bMf0…` — `gsk_` makes a leaked one
findable in a repository or a log, and both names make it actionable: a token
that says only "production" starts a hunt through every application. The
entropy is the 32 random bytes after them. Guard keeps only
`sha256(token)`, so the answer to the request that created it is the last time
guard can show it. Losing it means minting another, which is the point.

**Rotation without downtime**: mint a second key for the same environment,
deploy it, then revoke the first. Nothing limits an environment to one key.

Revoking keeps the row, marked and dated — it is the only record the key ever
existed, and "revoked in March" is the answer to somebody finding it in an old
deployment file next year. It takes effect on the **next fetch**, because the
vault looks the hash up every time.

### Why not a JWT

Because the vault has to read the database anyway to get the secrets, so the
lookup is free — one index hit — and everything a signed token would buy is
something guard would rather not have. Revocation is instant instead of
"whenever it expires". There is no signing key to distribute or rotate. There
are no claims that can disagree with the database about which environment a
token reads. It is the same bargain guard's own browser sessions make, for the
same reasons.

## The vault's surface

```
GET /v1/secrets              {"workspace":…,"env":…,"revision":…,"secrets":{…}}
GET /v1/secrets?format=env   KEY="value" lines
GET /v1/secrets/{key}        one pair
GET /healthz
```

Three rules carry it:

- **The workspace and the environment come from the key, never from the
  request.** There is no `?env=` and no `?workspace=`, and there never can be:
  a leaked staging key cannot be pointed at production, and no key of any
  application can read another's. A test tries five spellings of it.
- **Unknown, revoked and expired are one answer.** A caller that learns which of
  the three it hit has learned something about a token it does not hold.
- **Bookkeeping never fails a fetch.** Last-used and the read log are written on
  a throttle, and if that write fails the secrets still go out — an application
  that will not boot because an audit row would not fit is worse than a missing
  audit row.

`GUARD_TOKEN` does **not** open this door, and neither does a guard session
cookie. The exporter token is on every host in the fleet; the vault accepts only
a `gsk_` key. There is a test pinning that, because today it is true by
omission and omissions get helpfully fixed.

`ETag` is the environment's revision, so an application can poll every minute
for the price of a 304 and pick up a rotated secret without a redeploy.

## Using it from an application

Two variables replace whatever was injecting configuration before:

```bash
GUARD_VAULT_URL=http://vault.internal:4319
GUARD_VAULT_KEY=gsk_production_R-bMf0…
```

The key already means production, so there is nothing else to configure. Those
two are the one thing that cannot come from the vault — the bootstrap has to
live in the deployment.

Locally, skip the server:

```bash
guard-vault fetch -workspace hushkey -env local > .env
```

`-workspace` may be left out only while there is exactly one; with several it
refuses rather than picking, because a command that printed the wrong
application's values because they sorted first is worse than one that asks.

No token: whoever runs that already has the database and the key file, which is
everything a token would have protected.

## Running it

```bash
guard-vault                      # :4319, ./guard.db
guard-vault -db /data/guard.db   # the usual deployment
guard-vault fetch -workspace hushkey -env local   # print one, no server
```

## Two doors, and which one is the real one

The vault answers on **:4319**, and that is the door that matters: a second
process sharing nothing with guard but a database file and a key file, deployed
and restarted separately, so an application asking for its database password at
boot is not waiting on the dashboard's release.

`GUARD_VAULT_PROXY=1` adds a second one — `GET /v1/secrets` and
`GET /v1/secrets/{key}` on **guard's own port**, reverse-proxied to the first.
It is for the caller that cannot reach :4319: an application outside the VPC, or
one behind a proxy that terminates a single hostname. It is a proxy rather than
a second implementation, so there is exactly one place that checks a key, one
place that decides what an environment holds, and one read log — a test asks
both doors the same question and compares the answers, including the refusals.

**Off unless switched on**, and that is not caution for its own sake. Guard's
port is usually the published one — a domain, a certificate, a CDN in front —
and turning this on means a leaked key is usable from the internet rather than
from inside your network. Both the on and the off are said in the boot log, so
"why is /v1/secrets a 404" is answerable without reading this page.

Three things it does not do:

- **It adds no credential of its own.** The Authorization header travels
  untouched and the vault answers it. `GUARD_TOKEN` opens every write endpoint
  in guard's API and does not open this one, because guard never inspects the
  header — it forwards it.
- **It proxies two routes, not a prefix.** Forwarding `/v1/` wholesale would put
  whatever the vault grows later on the public port without anybody choosing it.
- **It sends no `X-Forwarded-For`.** The vault records the caller's address in
  its read log, and a header the caller controls is not the address — through
  the proxy it records guard's, honestly, rather than a number somebody typed.

A vault that is not answering is a **502 that says so**, because the two
processes restart on their own schedules and "the vault is down" and "your key
is wrong" must not look alike.

`GUARD_VAULT_ADDR` and `GUARD_DB_PATH` configure it; flags of the same names
override — and that is worth knowing before you type one into a unit file. A
typed flag beats the stored value *and* the environment, so a unit carrying
`-addr` makes the "Vault listen address" row on Settings → Configuration a
field that changes nothing. `deploy/guard-vault.service` therefore carries no
`-addr` at all. How often one key's use is recorded (a minute) is the server's own answer.

`make dev` starts it beside guard on :4319, under its own watcher
(`dev/vault`), sharing the terminal — its lines carry `app=vault`. A failed
build leaves the running vault alone, the same bargain howl dev makes. Set
`GUARD_VAULT_ADDR=` to leave it out.

In production it is its own systemd unit with `Restart=always` — it is
somebody's boot dependency. **Loopback is not an address for it.** The vault
exists to answer applications on *other* machines, so a unit binding
`127.0.0.1` leaves every one of them with `connection refused` while the box
itself answers perfectly, which is a confusing morning. Bind the VPC address or
leave it on every interface and let the provider's firewall group be the
perimeter, exactly as guard's own :4318 does.

### As a plain binary, which is the better default

The vault is a good candidate for running outside a container even where guard
is inside one, and the reasons are operational rather than about speed:

- **The container runtime stops being a boot dependency of your most
  boot-dependent service.** A wedged or upgrading daemon is exactly when
  applications are restarting and asking for their configuration.
- **SQLite sees the real filesystem** — no overlay, no volume driver, POSIX
  locks straight through. This is the one that actually bites.
- **Recovery is an exec**, not an image pull and a container create.
- It is pure Go (`CGO_ENABLED=0`, `modernc.org/sqlite`), so deploying it is
  copying one static file.

A fetch is an index hit and an AES-GCM decrypt of a few kilobytes, so there is
little in it CPU-wise; what a native process saves on the request path is the
runtime's NAT hop.

```ini
# /etc/systemd/system/guard-vault.service
[Unit]
Description=guard-vault — secrets for the applications
After=network-online.target

[Service]
# The address through the environment rather than a flag, so the configuration
# page stays the place it is set. A flag here would outrank it.
Environment=GUARD_VAULT_ADDR=10.0.0.5:4319
ExecStart=/usr/local/bin/guard-vault -db /var/lib/guard/guard.db
User=guard
Restart=always
RestartSec=2
# It writes: the WAL and shm beside the database, and its own bookkeeping.
ProtectSystem=strict
ReadWritePaths=/var/lib/guard
NoNewPrivileges=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

Bind it to the VPC address rather than `0.0.0.0`. Nothing about this wants to
be on the internet.

**If guard stays in a container and the vault does not**, they must be looking
at the same file, which means a **bind mount of a host directory** — not a
named volume — and matching ownership: the image runs as its own `guard` user,
the key file is `0600`, and a uid mismatch shows up as the vault refusing to
start. Same host, local disk, either way: two processes on one SQLite file is
only safe when both see the same inode and real file locks.

Guard owns the schema. On a brand new host the vault has nothing to read until
guard has opened the database once, and it says so and exits rather than
starting empty; `Restart=always` and the dev watcher both retry, so that is a
few seconds of noise on first boot rather than a failure.

## Backups

A backup of the database without `guard.db.key` beside it is not a backup of
the secrets — it is a file of noise that restores cleanly and hands out
nothing. That is the whole point of sealing them, and it is also the way a
restore goes wrong at the worst moment.

- **A volume or VPS snapshot is the right shape.** It captures `guard.db`, its
  `-wal` and `-shm`, and the key file in one go, which is both correct and the
  thing most likely to already be running.
- **Copying files while guard is writing is not.** `cp guard.db` alone can
  capture a state whose committed data is still in the WAL. Either stop guard,
  or take a consistent copy: `sqlite3 guard.db "VACUUM INTO '/backup/guard.db'"`
  — and copy the key file too, with its `0600` intact.
- **Restoring onto a new host means bringing the key.** Either the file, or
  `GUARD_SECRET_KEY` set to the same material. Without it, the pairs are there,
  the page lists every key name, and every value reads *unreadable* — which is
  guard telling the truth, not a corruption.

The key file is deliberately not in the repository and deliberately not in the
image. It belongs wherever your snapshots go.

## What it refuses to do

- **It will not generate a key file.** Missing `GUARD_SECRET_KEY` *and* missing
  `<db>.key` is a startup error. A vault that wrote itself a fresh key would
  come up perfectly healthy and answer every fetch with values it could not
  decrypt — which reads as corrupted secrets rather than as the unmounted
  volume it is.
- **It cannot change anything.** The store in `internal/vault` has no method
  that writes a secret, an environment or a key, so no handler above it can grow
  one by accident. The only writes are the two lines of bookkeeping.
- **It does not serve the dashboard, ingest telemetry or talk to the cluster.**
  It imports none of it. That is what makes "guard is down, secrets are up" a
  property of the build rather than a hope.
