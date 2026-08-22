# Deploys

Guard puts a versioned image on the machines it already watches. Nothing here is
a new way to reach a box: the login is the one the cluster page stored, the
health check is the one the prober has been making all along, and the alert
leaves through the same `internal/notify` every other watcher uses.

The page is **/deploys**. The code is `internal/deploy` (the runner),
`internal/telemetry/deploy.go` (the store), `server/apis/deploy/` (the
endpoints) and `client/public/deploys.js` (the page).

## What a deploy actually does

For each machine, over SSH, in one command:

1. `mkdir -p <template path>`
2. write `docker-compose.yml` — 0644, from the template version being deployed
3. write `.env` — **0600**, `TAG=<tag>` first, then the template's variables
4. `docker compose pull <service>`
5. `docker compose up -d --no-deps <service>` — or `up -d --remove-orphans` over the
   whole file, when anything the file declares is not running
6. poll the health target until it passes or the deadline runs out

Both files are written to a temporary name beside the target and renamed over
it, with the previous one kept as `.guard-bak`. The content travels as base64 —
not because it is secret but because a compose file is full of colons, quotes
and dollars, and every bug in a thing like this is a quoting bug.

`docker compose` or `docker-compose`, whichever the box has; a machine with
neither says so and exits 127 rather than looking like a pull failure.

**Only the service the template names is recreated**, and that is step 5's whole
point. Guard rewrites the `.env` on every deploy, so its hash changes, so a
plain `up -d` recreates every service that reads it — including a reverse proxy
holding `:80`, which is then racing its own outgoing container for the port and
sometimes loses:

```
Bind for :::80 failed: port is already allocated
```

A deploy that changed one image has no business restarting a proxy. But
`--no-deps` cannot be the only shape either: a fresh box, or a compose file that
has grown a service, needs everything started. So the file is asked first —
`config --services` against `ps -q` — and a project with anything missing gets
the full `up`. The trade-off is stated rather than hidden: change a sidecar's
configuration and it applies on the next deploy that brings the project up, not
on the next deploy.

**A port that is still held is retried once, through compose.** `down
--remove-orphans` then `up -d --remove-orphans`, scoped to this project in this
directory. That cures the one case a retry can cure — docker not having released
the outgoing container's binding yet — and gives up rather than looping, because
a port held by something that is not ours never comes free.

What guard will **not** do is go looking for the process on the other end of
`lsof -ti :80` and kill it. On a docker host that process is usually
`docker-proxy`, and killing it leaves the daemon believing the mapping is still
live; on a box where it is somebody's nginx, it is an outage guard caused. The
port is a symptom, and the fix for it belongs in the compose file or on the box
— not in a deploy that guesses.

## Healthy means the health check passed, and nothing else

Three consecutive successes, five seconds apart, two minutes to do it in
(`model.HealthPasses`, `HealthInterval`, `HealthDeadline`). Consecutive, because
one pass is a process that has not fallen over yet. A deadline, because a check
that never resolves has to become a failure rather than a run sitting in
`health_check` forever.

**It is deliberately not an error-rate check.** A container twenty seconds old
has served nobody, and guard's rule everywhere else is that an empty window is
*silence*, not zero — so a rate-based gate would read "no traffic yet" as "no
errors" and pass a corpse. Reading it the other way, as a failure, would break
every low-traffic node. Error rates are worth alerting on; they are a monitor
rule on its own loop, after the deploy, not a gate.

**The health path belongs to the template, not to the machine.** The box's own
health path answers for whatever was there before, and a deploy has to be proved
by the thing it deployed. A template can also name a port, for a service that
answers somewhere the machine's address does not. Empty falls back to the
machine's own path, which is the ordinary case: one service on the box, already
being watched.

**A machine with no address cannot pass.** "Cannot check" is not "passed" — a
gate that treated it as one would be a gate in name only.

## Sequential, and what happens when it stops

Sequential is the default and the only mode that protects anything: one machine
at a time, each proved healthy before the next is touched.

On a failure the run **stops** — nothing after it is touched — and:

- it moves to `awaiting` and **tells the group's destination immediately**;
- it says so again after **fifteen minutes** (`model.AwaitingAlert`);
- it **gives up after thirty** (`model.AwaitingDeadline`), releasing its locks
  and recording that the machines it never reached were never reached.

That third line is the point. "Waits until resolved" with nobody watching is a
stuck deploy holding a lock nobody can clear. A group with no destination is
allowed — it is the same bargain a rule with no destination makes — but then a
stopped run waits in silence, so the log says so and the group dialog says so
beside the field.

The three answers are **retry**, **skip and continue**, and **stop**. There is
no fourth, and there is deliberately no autonomous rollback: guard surfaces what
happened and a person presses the next thing.

## Retrying one that is over

A run that ended anything other than healthy gets a **Retry** button. It starts
a **new run** rather than resurrecting the old one — the failed row is the only
account of what happened, and rewriting it would lose that — and it does it by
filling in the ordinary deploy dialog: same template *version*, same tag, same
mode, one press to confirm.

Two details it gets right that a person re-picking from the group would not:

- **the version is pinned to the one that ran**, not the newest. Repeating a run
  against a template somebody has edited since is not a repeat.
- **only the machines that did not come back healthy** are named. Redeploying
  the ones that already passed their gate would be replacing working containers
  to fix a different machine. If every machine failed, it is the whole group
  again, and the summary says so.

The mode is carried over too, and that is the one place a remembered "all at
once" is right: a retry repeats something that already happened rather than
choosing afresh. Everywhere else the dialog opens gated.

## Stopping one that is still going

**Stop** is on any run that is `running` or `awaiting`
(`POST /api/deploy/cancel`). Each run gets its own context, so pressing it
reaches the SSH session and the health gate rather than setting a flag nothing
is reading — a two-minute gate is cut immediately instead of running down its
clock.

What it does and does not do is the whole of the design, because it is a button
somebody presses during an incident:

- it stops guard **advancing** — no machine after the current one is touched;
- it stops guard **waiting** — the gate and the session are cut;
- it **undoes nothing**. A machine already deployed to keeps what it was given,
  and the machine in flight may have a container running that guard never
  proved.

So the run is recorded as `cancelled` rather than `failed` — nothing broke, a
person decided — and the machine in flight is `interrupted` with *"the container
may be running, guard stopped before it was proved"*. Calling that a failure is
how a healthy box gets restarted at 3am.

Going back is a deploy of the last known good tag: the ordinary press. There is
no undo, and there should not be one — the only honest way back is forward
through the same gate.

**Parallel gates nothing.** Every machine at once, health still checked and still
recorded, blocking nobody. The button says what that costs, because the option
labelled "fast" is the one that gets picked out of habit.

## Rollback is a deploy

There is no rollback mechanism. Rollback is the ordinary press with the
machine's `last_known_good_tag` filled in and that one machine named — one
endpoint, one code path, the one that is exercised every day. A separate path
would be the one used least often and tested least, on the worst day.

A rollback target is **the deploy before the current one**, and it is a pair:
the tag *and* the template version. Both are written only on a passed health
gate, so both halves are things that actually answered.

The pairing matters because what changes between two deploys is often the
compose file rather than the image — anybody deploying `latest`, or a fixed base
image with an edited template, changes only the version. A rollback that
restored the tag but not the file would put back a combination nobody ever ran.

Stepping is what makes it useful: on each success the old current becomes last
good. The first version of this moved both together, which made them permanently
equal and rollback a no-op in **every** case — a failed deploy leaves the machine
on the last good thing already, so there is nothing to go back to, and a
successful one would name itself as its own rollback target. Redeploying an
identical tag and version does not shift last good, because stepping back to an
identical thing is not a step.

## The lock reaches deploys

A deploy writes files and runs docker. It is the command line wearing a
template's name, so a **locked machine refuses it** — enforced in
`Store.DeployTarget`, not in a handler, like every other lock check. A group may
still *hold* a locked machine or one with no login; that is a normal state on
the day a box is added, and the page marks it so nobody lines up a deploy that
cannot run.

The whole group is checked before anything is touched: a rolling deploy that
stops at the third machine because it is locked has already replaced two.

## Secrets

A template's variables come from one of two places, and the difference is where
the value is at rest:

- **static** — stored with the template, written into the `.env`. A port, a log
  level, a public URL. It travels in the backup and is readable on the page, so
  nothing here is secret.
- **vault** — a key in the template's secret environment, resolved **at deploy
  time** and written into the `.env` on the target. Guard keeps no second copy.
  The file is 0600 and owned by the account guard logs in as — but it is
  plaintext on that box, and the dialog says so.

**A variable has to be delivered, and the compose file is what delivers it.**
This is the sharpest edge here and it used to be silent. Guard writes the
variables into a `.env` beside the compose file — and docker compose reads that
file to fill in `${...}` **in the compose file itself**. It does not put those
values inside the container. A service that says neither `env_file:` nor
`environment:` starts with none of them, and an application written to fall back
on a default starts against the default: the wrong database, or none, while the
container is up and answering, so the health gate passes it.

So the service that is deployed needs one line:

```yaml
services:
  app:
    image: syd.vultrcr.com/hushkey/pack:${TAG}
    env_file: .env
```

A template that declares a variable the compose file never mentions and never
reads as a file is now **refused at the save**, naming each one — the same rule
as a compose file that never mentions `${TAG}`, and for the same reason: it
looks like a successful deploy of something it did not do. The check is
generous, so `env_file`, `${DATABASE_URL}` under `environment:`, and a bare
pass-through entry all pass.

**The third option is to declare nothing.** A variable your application fetches
from `guard-vault` itself with its own scoped key does not belong in the
template: the key is already on the machine (put there by the machine's
environment inject), the deploy never sees the value, and rotating it stops
being a redeploy. Where an application can do that, it is the better answer.

One environment per template, so a staging template cannot resolve a production
value — the same rule a vault key keeps. The values are resolved **once** for
the whole run, before any machine is touched: a key that is missing stops the
run at the press, and a key rotated mid-rollout cannot give the second half of
the fleet a different environment to the first.

## A template is three answers

A name, a compose file, and where to health-check. Everything else is derived at
the save and then stored:

- **the directory** is `/guard/<name>` — one directory guard owns, a subdirectory
  per template, so "what did guard put on this machine" is one `ls`;
- **the service name** — the key `deploy_state` is written under — is the name's
  slug;
- **the image** is the one the compose file tags with `${TAG}`.

The last one is worth more than the saved keystrokes. A separate image field can
disagree with the compose file — naming one repository while the file pulls
another — and nothing would notice, because guard never reads that field at
deploy time. Deriving it means the tag list on the deploy dialog is always the
tag list of the image that is actually going to be pulled.

A compose file that never mentions `${TAG}` is **refused**: deploying it would
pull whatever it already said and change nothing, which looks exactly like a
successful deploy of the wrong version. One that tags two different images is
refused too — a template deploys one image, and two of them is two templates.

All three are still accepted over the API when sent explicitly, for a template
that has to live somewhere specific. They are derived **once, at the save**, and
stored: a run months old has to say where it wrote and what it pulled, and
recomputing a path later would leave the old containers running in a directory
guard had forgotten about.

**The service comes off the compose file.** It is the one whose image carries
`${TAG}` — derived like the image itself, and for the same reason. The template's
name is what a person calls the deploy ("PACK-APP"); the service is what the file
calls the container ("app"), and nothing makes those the same string. Guard used
to slug the name, which was invisible while a deploy ran `up -d` over the whole
file and became `no such service: pack-app` the moment it addressed one. A
template naming a service its compose file does not declare is refused at the
save, and a template stored before this says so in its own deploy output and
deploys the tagged service instead.

## Making a machine deployable

A box with no `docker compose` fails a deploy with `docker exited 127: no docker
compose on this machine`. **Install docker** on the machine's row answers it,
over the login guard already proved.

It is `internal/deploy/prepare.go`, and it is `internal/envfile`'s shape: the
request carries a **node id and nothing else**, the command is a constant in
guard's source, so there is no version of the call that runs chosen text on a
chosen box. Three branches, in order: already working (one round trip, nothing
installed), docker present but no plugin (the distribution's
`docker-compose-plugin`), no docker at all (Docker's own `get.docker.com`
installer — a script run as root, downloaded to a file first, which is worth
saying out loud rather than hiding).

It goes through `DeployTarget`, so a **locked machine refuses it**: installing
packages as root is exactly what a lock is for. It is idempotent, so pressing it
on a machine that is fine is free.

It is deliberately **not part of a deploy**. A deploy that quietly installed a
package manager's worth of software the first time it ran would be doing
something nobody asked for on the worst possible day.

For a box that does not exist yet, `deploy/cloud-init-app.yaml` is the same
thing at creation time: docker from Docker's apt repository, `/guard` created,
keys-only SSH, and no application — because guard owns the compose file and
writes it on the first deploy.

## Output while it is happening

`docker compose pull` says nothing for a minute and then says a lot. So the SSH
runner streams (`remote.Runner.Stream`) and the deploy writes what it has so far
into the instance row about once a second — under the three the dashboard ticks
at, so the pane is never more than one tick behind.

There are two channels, and the layering is the point:

- **The row is the truth.** It is what a page reloading mid-deploy reads, what a
  second person sees, what is there tomorrow, and what a browser that never
  opened a stream falls back to. Written about once a second.
- **`GET /api/deploy/stream` is the fast path.** An event stream
  (`?run=<id>`, or `?node=<id>` for an install) carrying a frame per chunk of
  output, so a pull reads like a terminal instead of jumping once a second. A
  raw handler rather than an endpoint, because the typed layer answers with a
  value and this answers with a connection that stays open; `app.SSE` owns the
  wire format.

Nothing on the stream is authoritative, and that is what makes it safe to have
both. `internal/deploy/stream.go` sends **non-blocking and coalescing**: a
watcher that is behind gets the newest frame and misses the ones in between,
because every frame carries the whole output so far. The SSH reader must never
wait on a socket in somebody's browser — a deploy that stalled because a laptop
went to sleep with a tab open would be the worst bug this feature could have.
A dropped connection costs nothing: the browser reconnects on its own, and the
tick has the row either way. A frame marked `done` is the last one, so the page
closes the socket instead of holding one open for a finished deploy.

A frame's `done` is about the **run**, never a machine. A watcher closes its
connection when it hears it, so marking each machine done ended the stream at
the first one to finish and left the rest of a rolling deploy unwatched. The one
`done` frame is published after the run's final status is written, so a watcher
that reacts by re-reading the row gets the finished one — which is what makes
the Stop and Retry buttons settle the instant a run ends rather than on the next
tick.

There is a heartbeat every twenty seconds, because the interesting part of a
deploy is the two minutes where nothing is printed and every proxy in the world
closes an idle connection somewhere inside that.

**One framework fix went with this.** `mw.Coalesce` buffers a response to share
it between identical concurrent GETs, and an event stream has no end to buffer
up to — so on an instance with no sign-in (no cookie, no token) an `EventSource`
request was being recorded and the browser saw nothing until the stream closed,
which for SSE is never. `shareable` in howl-go now refuses `Accept:
text/event-stream`.

## How much history is kept

Fifty runs **per group** (`deployRunRetention`), trimmed at the moment a group
gains a run — which is the only moment its history can have grown too long. Per
group rather than overall, so a busy application cannot push a quiet one's
history out of the table.

An unfinished run is never pruned, whatever its age: a run still going, or still
waiting for somebody, is the one row that must not vanish from under the page
watching it.

The page shows three at a time. A deploy row is tall — a machine list, a
progress bar, an error and a log pane — so a page of them is a page rather than
a list. `GET /api/deploy/runs` takes `limit` and `offset` and answers with the
rows and the total, so the pager knows whether to enable a button without a
second request. The **active** runs are never paged: there are at most a handful,
and a pager over the thing somebody is watching would be absurd.

## Private registries

Guard's stored provider key is what **guard** uses to call the provider's API —
listing registries, repositories and tags for the dialog. It is not what the
*machine* uses to pull. A private image needs a `docker login` on the target
box; a public one needs nothing, which is why a Docker Hub image works with no
setup at all.

## Templates are versioned, and guard owns the file

Saving a template never overwrites. It writes the next version, and a run pins
the version it deployed — so "what did we actually deploy" is answerable months
later even if the template has moved on since.

The compose file lives **in guard**, not on the box. That is the opposite of how
a machine's environment works, and it buys one thing: the template travels in
the backup, so a replacement machine is provisioned by deploying to it rather
than by somebody remembering what was in `/srv` last year.

Guard still writes only what guard rendered. A deploy request carries a template
id and a tag — never file content — which is what stops this becoming a way to
run a chosen file on a chosen box. The directory is checked by
`model.ValidateDeployPath`: absolute, no traversal, nothing a shell would read,
and not one of the directories that would take the machine down with it.

## Locks, restarts and what is never resumed

One lock per machine, held in memory, taken for the whole run. A second deploy
at a machine already being deployed to **fails loud** — there is no queue,
because two people deploying different tags to one box is not something to
serialise.

In memory on purpose: a restart releases every lock, exactly like the
scheduler's `running` map. What a restart leaves in the database is made honest
at startup by `SweepDeployRuns` — machines mid-deploy become `interrupted`, ones
never reached become `skipped`, and the run becomes `interrupted`.

**Nothing is resumed.** Guard cannot know whether `compose up` finished, and a
resume that assumed either way would be guessing about somebody's production.
`current_tag` is untouched, so the dashboard keeps saying the last tag that
actually answered rather than the one that was on its way.

## Timeouts

`deploy.Timeout` is **ten minutes** for one machine's whole deploy — against the
two a pressed command gets and the thirty a scheduled dump gets. Pulling a fat
image over a slow link is a legitimately long thing to be doing, and the two
minutes a `remote.Runner` defaults to would have returned that as a spurious
failure.

A constant beside the code that reads it, like every other cadence in guard.

## What travels in a backup

`deploy_groups`, `deploy_group_nodes`, `deploy_templates` and `deploy_state` —
the configuration, and what each machine is running. `deploy_runs` and
`deploy_run_instances` do not: they are history about the instance that is gone,
the same reason `cluster_runs` stays behind.

## Endpoints

| | |
|---|---|
| `GET /api/deploy/groups` | groups with their machines |
| `POST /api/deploy/groups` | create or replace one, membership and all |
| `DELETE /api/deploy/groups/{id}` | remove one; the runs keep its name |
| `GET /api/deploy/templates` | newest version of each, with its history |
| `POST /api/deploy/templates` | write the next version |
| `GET /api/deploy/templates/{id}?version=` | one version, exactly as deployed |
| `DELETE /api/deploy/templates/{id}` | every version |
| `POST /api/deploy` | **start a run** — group, template version, tag, mode |
| `POST /api/deploy/resolve` | answer a stopped run: retry, skip, stop |
| `GET /api/deploy/runs?active=true` | what is going on now |
| `GET /api/deploy/runs/{id}` | one run, with a row per machine |
| `GET /api/deploy/state?node_id=` | what one machine is running |

All admin. `POST /api/deploy` answers with the plan immediately rather than
holding the request open for a rolling deploy — a request that waited for five
machines would be a deploy that fails when somebody's laptop sleeps.
