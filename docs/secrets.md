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

`GUARD_VAULT_ADDR` and `GUARD_DB_PATH` configure it; flags of the same names
override. `GUARD_VAULT_TOUCH` (1m) is how often one key's use is recorded.

`make dev` starts it beside guard on :4319, under its own watcher
(`dev/vault`), sharing the terminal — its lines carry `app=vault`. A failed
build leaves the running vault alone, the same bargain howl dev makes. Set
`GUARD_VAULT_ADDR=` to leave it out.

In compose it is the same image with `entrypoint: guard-vault`, mounting the
same volume, with no published port and `restart: always` — it is somebody's
boot dependency.

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
ExecStart=/usr/local/bin/guard-vault -db /var/lib/guard/guard.db -addr 10.0.0.5:4319
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

Guard owns the schema. On a brand new volume the vault has nothing to read until
guard has opened the database once, and it says so and exits rather than
starting empty; compose starts it second and the dev watcher retries, so that is
a non-event rather than a failure.

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
