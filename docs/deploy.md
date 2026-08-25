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

Locally, `make` stamps `git describe --tags --dirty` instead, so a development
build reports the commit it came from — `v0.1.0-7-g741bc0a-dirty` — and `make
dev` passes the same thing through `GOFLAGS`. Nothing stamped at all (a plain
`go run .`) reports `v0.0.0-dev`. The default in `internal/build` used to be the
current release number, hardcoded, which meant every release made it staler: a
development binary claimed an *older* version than the checkout it was built
from, and the update card then offered an upgrade to a release the tree was
already past. A version guard has to be told is a version somebody forgets to
tell it.

The sidebar card stays hidden for those builds (`build.IsDevelopment`). It
compares versions by difference rather than by ordering — republishing an older
tag is how a fleet is rolled back — and a working tree differs from every
release by construction, so without that check the card would live permanently
in every checkout's sidebar.

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

`deploy/cloud-init.yaml` is the whole of it: paste it into the provider's user
data field and the box comes up serving the dashboard on :4318, with nothing to
configure on the machine.

That is only possible because guard's configuration moved into guard's database.
An earlier cloud-init here set up nginx, opened ports and wrote an env file, and
was deleted for being a set of decisions this repository does not get to make —
every one of them drifting from the copy here the moment somebody made it
differently. What is left is the part that is the same on every box: a user, two
directories, the units, the updater, and the first install.

It comes in two shapes, decided by two lines at the top of the script it writes:

| `GUARD_DOMAIN` | what you get |
|---|---|
| empty | guard answers :4318 directly — dashboard and OTLP on one port, nothing else installed |
| set | nginx on 80/443 for that name, and a certificate that issues itself once DNS points here |

The second is not optional decoration: a box reached through a domain has to have
something listening on 443, and a provision that installs guard perfectly while
leaving nothing in front of it is a box that looks broken from every browser.
Guard still answers :4318 on the box's own address either way, because a
collector on another machine posts to the address rather than to the domain —
that is the whole reason the unit does not bind loopback.

- **The units and the updater are fetched from the repository**, not inlined into
  the file. A systemd unit written down twice is one that is eventually wrong in
  the copy nobody is looking at — which is how a box ends up with guard bound to
  localhost for nginx's sake and a collector on the next machine unable to reach
  it. `GUARD_REF` at the top pins which ref they come from; re-running
  `/usr/local/sbin/guard-bootstrap` is how a box picks up a change to one.
- **The certificate issues itself, on a timer, and stops.** Certbot inside the
  provisioning run is certbot against a name that does not resolve yet, because
  the A record is usually typed after the box exists. The timer retries every
  fifteen minutes — not five, because a failed issuance counts against Let's
  Encrypt's five-an-hour for that name, and a five-minute retry spends the budget
  before DNS is ready — and disables itself once the certificate is there. It
  asks only whether the name resolves at all, never whether it resolves to this
  box: behind Cloudflare's proxy it never will, and a check that compared the two
  would keep a proxied domain from ever being issued.
- **No firewall is touched.** The public perimeter is the provider's — a Vultr
  firewall group filtering the public interface — and two allowlists that have to
  agree is how somebody gets locked out editing one over the other.
- **It does not close the door, and says so.** Guard answers :4318 for the
  dashboard *and* for OTLP, and with no sign-in configured anyone who can reach
  the port can do anything — which is what makes the first run possible. So
  either the firewall group does not expose 4318 yet, or sign-in is configured in
  the same sitting, from Settings → Security.

Then the dashboard: sign-in and members, the two tokens (both have a Generate
button), the alert destinations, retention. Each save says "restart to apply",
and the restart is a button.

What that file does, if you would rather do it by hand — or want to know what it
did:

- **A `guard` user** and `/var/lib/guard` for the database, its WAL and the key.
- **`/etc/guard` owned by root**, with `version` handed to the guard user. That
  split is the whole permission model: guard rewrites the version file and
  cannot touch `guard.env` beside it. Everything else guard changes about itself
  now lives in its database — see [configuration](config.md).
- **The units and `guard-update`**, then `systemctl enable --now`. The
  `--now` is load-bearing — `enable` alone wires a timer to `timers.target`,
  which a provisioning script reaches *after* systemd has passed, so the box
  comes up correct and never takes another release until it is rebooted. It
  fails silently, and looks exactly like success.
- **The first install is an ordinary update.** Run `guard-update` rather than
  placing binaries by hand: same download, same checksum, same health gate. A
  first-boot special case is a code path that stops being exercised the moment
  it works.

Two things no provisioning can do for you. The **key**: guard generates
`/var/lib/guard/guard.db.key` on first start, and joining a box to an existing
database means putting that key there first. And the **door**: guard listens on
:4318 for the dashboard and for OTLP, and what fronts that is your decision —
along with which of them the network can reach, since `/v1/*` sits outside
sign-in by construction and answers to whatever can route to the port.

## By hand

Install the units and the updater once:

```bash
install -m 0755 deploy/guard-update /usr/local/bin/guard-update
install -m 0644 deploy/*.service deploy/*.timer deploy/*.path /etc/systemd/system/
useradd --system --home /var/lib/guard --create-home guard
systemctl enable --now guard guard-vault guard-update.timer guard-update.path
```

`/etc/guard` stays root's, with only `version` handed to the guard user — that is
the whole of what guard writes outside `/var/lib/guard`. Its own configuration
goes in the database, so there is no env file to create and no directory to hand
over for it.

An Update now request changes `/etc/guard/version`, which `guard-update.path`
notices immediately and starts the updater. The fifteen-minute timer remains as
a recovery sweep for missed events and versions changed while the path unit was
not running, with a randomised delay so a fleet does not ask GitHub in lockstep.

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

Guard asks every fifteen minutes and writes `/etc/guard/version`; neither is
configurable, because the interval is tuned to somebody else's rate limit and the
path is hardcoded in `guard-update` anyway.

`/etc/guard/version` is the whole interface:

| contents | what happens |
|---|---|
| absent, or `latest` | the box follows the newest release |
| `v0.2.0` | the box installs and stays on that, ignoring newer ones |

So pinning a box is one line. The thing that replaces binaries stays outside
guard, under its own unit, and keeps working on the day guard does not start,
which is the day you want it most.

### Rotating the two tokens

`GUARD_TOKEN` and `GUARD_OTEL_SECRET` arrive from the environment, which used to
mean: SSH in, `openssl rand -hex 32`, edit an env file, `systemctl restart guard`.
Four steps, three of them a shell, and the honest outcome is that neither is ever
rotated.

They are now two rows of the [configuration](config.md) page with a **Generate**
button on them — the only rows that have one, because they are the only values
guard itself issues. Press it, press **Restart Guard**, and the new token is what
the process is running. The value is shown in full and copied to the clipboard:
the collector secret exists to be pasted into a collector on another box.

Boxes provisioned against an earlier release have a `/etc/guard/env.d/tokens.env`
that guard used to write. It keeps working — the unit reads it, so it is simply
the environment — and the first value generated after this release outranks it.
The file can then be deleted.

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
- **Answering is not enough — it has to answer as the new version.** After the
  health check passes, the installed binary is asked `-version` and must say the
  tag that was wanted. Two real failures hide behind a healthy port: an install
  that did not land (so the *old* binary is what answered), and something else
  listening on 4318 entirely. Both used to read as a successful deploy, and a
  deploy that quietly did nothing is worse than one that failed loudly.
- **Every step is checked by hand, not left to `set -e`.** `swap` is called as
  `swap … || die …`, and a command in an `||` list runs with `-e` suspended —
  inside the function body too. So a failed `mv` there would not have stopped
  anything.
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
