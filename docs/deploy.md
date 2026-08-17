# Deploying

Guard ships as two static binaries and no runtime dependencies. Everything the
dashboard needs — the pages, the stylesheet, the wasm bundle — is embedded, so a
host needs the files and nothing else:

```
/usr/local/bin/guard          33MB   the dashboard, OTLP intake, cluster loops
/usr/local/bin/guard-vault    10MB   secrets, and nothing else
/var/lib/guard/guard.db       the database, its WAL, and guard.db.key beside it
```

Binaries rather than containers, deliberately — there is no Dockerfile here and
no compose file. The container runtime stops being a boot dependency of the most
boot-dependent service you have, SQLite sees the real filesystem instead of an
overlay, and recovery is an exec rather than an image pull. There is nothing to
build on the box: pure Go, `CGO_ENABLED=0`, so one runner cross-compiles for
every architecture in about a minute.

## The pipeline

```
git tag v0.2.0 && git push --tags
        ↓
GitHub Actions: cross-compile both binaries for linux/amd64 and linux/arm64,
                stamp the version, write SHA256SUMS, publish a release
        ↓ (the box polls; public repo, so no credential anywhere)
guard-update on a timer: read the wanted version, verify, install, restart
```

Push-free on purpose. The box reaches out, so there is nothing inbound to open,
no deploy key to distribute and no SSH. Artifacts live in GitHub Releases —
free, CDN-backed, and kept until you delete them, so every past release is a
rollback target.

Assets are at predictable URLs:

```
https://github.com/hushkey-app/guard/releases/download/v0.2.0/guard-linux-amd64
https://github.com/hushkey-app/guard/releases/latest/download/guard-linux-amd64
```

**The version is stamped into the binary**, so `guard -version` and the sidebar
and `/api/openapi.json` cannot disagree about what is running. The updater asks
the binary rather than keeping its own note, which is the note that goes stale
the one time somebody copies a file by hand.

### The framework checkout

`go.mod` still carries `replace github.com/mirairoad/howl-go => ../howl-go`, and
that is not stale: the published `v0.1.0` of the framework predates `core/api`,
`core/state`, `core/console` and `core/mw`, which guard imports. So the workflow
checks howl-go out beside guard and the replace resolves.

**The ref must contain them.** `main` did not when this was written — the four
packages live on a branch — and a workflow pointed at a tree without them fails
with "no required module provides package", which is what it looks like when
the framework's code has not left somebody's laptop. `HOWL_REF` at the top of
the workflow is the one line to change.

When howl-go publishes a tag containing those packages, delete the second
checkout and the replace together — that also unblocks
`go install github.com/hushkey-app/guard@latest`, which the replace refuses.

## A new box

`deploy/cloud-init.yaml` is the whole provisioning: paste it as user-data, boot,
and the box comes up running both services. It installs no toolchain and builds
nothing — it writes the units, fetches `guard-update`, and then runs it, so the
first install goes down exactly the same path as every later one. A first-boot
special case is a code path that stops being exercised the moment it works.

Two knobs at the top: which version to follow or pin, and where the vault
listens — `127.0.0.1` serves that box alone, the private address serves the
VPC, and `0.0.0.0` serves the internet, which is not what this is for.

It cannot do two things for you. The **key**: guard generates
`/var/lib/guard/guard.db.key` on first start, and joining a box to an existing
database means putting that key there first. And the **door**: guard listens on
:4318 for the dashboard and for OTLP, and what fronts that is your decision.

## By hand

Install the units and the updater once:

```bash
install -m 0755 deploy/guard-update /usr/local/bin/guard-update
install -m 0644 deploy/*.service deploy/*.timer /etc/systemd/system/
useradd --system --home /var/lib/guard --create-home guard
install -d -o guard -g guard -m 0700 /etc/guard/env.d
systemctl enable --now guard guard-vault guard-update.timer
```

That one directory is guard's own; `/etc/guard` around it stays root's. It is
where the dashboard writes the credentials it generates for itself, and a box
without it simply gets a card that shows what is set and no buttons.

Everything after that is the timer. It checks every fifteen minutes, with a
randomised delay so a fleet does not ask GitHub in lockstep — unauthenticated
API calls are capped at 60 an hour per address, and four an hour per box leaves
room for several boxes behind one NAT.

### The Update button

Guard polls the releases API every fifteen minutes — server-side, so a dashboard
open in four tabs still costs one request a quarter of an hour rather than four,
which matters against a sixty-an-hour budget shared with the updater on the same
address. When a release differs from the running version, a card appears at the
bottom of the sidebar, above the settings card.

Pressing **Update** writes the version into `/etc/guard/version` and nothing
else. It does not install and it does not restart: the card then says
"requested", because the install is `guard-update` on its own timer and may be
a quarter of an hour away — and one of the things it restarts is the page the
button was pressed on.

Guard will only write a version it has actually seen from the API, or `latest`.
The file is read by something running as root that puts the value in a URL, so
the set of things it may contain is the set guard has been told exists, rather
than "a string that looks like a version" — which is a validator somebody
eventually widens.

On a box with no `/etc/guard` — a container, a laptop, `go run .` — the card
still names the release and links to it, but offers no button, because there is
nothing to write and no unit to act on it.

| variable | default | what |
|---|---|---|
| `GUARD_UPDATE_REPO` | `hushkey-app/guard` | empty watches nothing at all |
| `GUARD_UPDATE_INTERVAL` | `15m` | how often guard asks GitHub |
| `GUARD_UPDATE_STATE` | `/etc/guard/version` | the file the updater reads |

`/etc/guard/version` is the whole interface:

| contents | what happens |
|---|---|
| absent, or `latest` | the box follows the newest release |
| `v0.2.0` | the box installs and stays on that, ignoring newer ones |

So pinning a box is one line. The thing that replaces binaries stays outside
guard, under its own unit, and keeps working on the day guard does not start,
which is the day you want it most.

### The credentials card

`GUARD_TOKEN` and `GUARD_OTEL_SECRET` arrive from the environment, which means
they arrive from a file on the box — so rotating one has always been: SSH in,
`openssl rand -hex 32`, edit an env file, `systemctl restart guard`. Four steps,
three of them a shell, and the honest outcome is that neither is ever rotated.

**Settings → Credentials** is those steps. Guard writes one env file of its own,
`/etc/guard/env.d/tokens.env`, and the unit reads it **after** `guard.env` — so
a value generated from the dashboard wins over the same name set by hand, and
the button cannot quietly lose to a line somebody wrote a year ago. `guard.env`
itself stays root-owned: the OAuth credentials, the database key and everything
else typed by hand are not guard's to rewrite.

Both values are shown **in the clear**, and that is the one place this differs
from every other credential guard holds. An SSH password is one guard uses on
somebody's behalf and is never read out; these two are values a person has to
paste into a collector on another box, and a card of dots is a card that sends
them back to the shell it replaces. Every endpoint behind it is `admin`, reads
included, so nobody reads this who could not already read everything.

Writing is not applying. A process has its environment from the moment it
started, so the card says **generated · restart to apply** and offers a second,
separate press — the restart drops the dashboard it was pressed on and anything
in flight at ingest, which should be chosen rather than stumbled into.

Guard restarts by **exiting**. It runs unprivileged with `NoNewPrivileges` and
could not call `systemctl` if it wanted to; the unit's `Restart=always` starts
it again two seconds later, against the new file. The button therefore appears
only under systemd — guard asks for `INVOCATION_ID`, which is the supervisor's
own word rather than a setting somebody has to keep true. Anywhere else,
exiting is just stopping, so the card says to restart the service by hand.

| variable | default | what |
|---|---|---|
| `GUARD_ENV_FILE` | `/etc/guard/env.d/tokens.env` | the env file guard may write |

Rotating the collector's secret and rotating the operator's token are separate
presses, because they are separate days: one is a box being decommissioned, the
other a laptop being lost, and doing both because one was asked for stops every
collector in the fleet at once. **Clear** takes a name back out of the file, so
the next start falls back to `guard.env` or to nothing — which on an instance
where nobody signs in reopens every write endpoint, so the dashboard asks first.

## What the updater guarantees

- **Verify before installing.** The published `SHA256SUMS` is checked against
  the two files actually downloaded. A truncated download is the failure that
  really happens, and it is caught before anything is replaced.
- **Guard first, then the vault.** Guard owns the schema and migrates on start;
  a vault started against a table guard has not rebuilt yet is the one ordering
  that breaks. They are always the same version, from the same release.
- **Roll back on a failed health check.** Each service is restarted and then has
  thirty seconds to answer `/healthz`. If it does not, the previous binary is
  put back and restarted — kept beside the new one, so going back is a rename
  rather than a download. If guard fails, the vault is never touched.
- **A rename, never a copy over.** A running binary cannot be written over
  (`ETXTBSY`), and rename is atomic, so there is no moment where the file on
  disk is half a binary.

It is a shell script rather than a Go program because the updater has to work
when the binaries do not: no build step, nothing to update itself, and its only
dependencies are curl, sha256sum and systemctl.

## What is never in an artifact

The database and `guard.db.key`. A deploy replaces binaries and nothing else.
Back both up together — a database without the key beside it restores cleanly
and hands out nothing, which is covered in `docs/secrets.md`.
