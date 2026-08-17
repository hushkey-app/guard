# Configuration

Guard has always been configured the way a container is: `GUARD_*` in the
environment, a flag of the same name to override it. That is right for the
process and wrong for the person — every change is an SSH session, a file
somebody has to remember the name of, and a restart typed by hand. The value
that ends up in that file is usually copied out of a document listing the names,
so guard may as well *be* that document.

**Settings → Configuration** is the handful of those somebody actually changes, as
a form. The catalogue is `internal/config`'s `Entries`; the page is drawn from what
`GET /api/config` answers, so adding a row is one entry there and nothing else.

The first version listed all thirty-odd `GUARD_*` variables, on the theory that a
form which is the complete list can never be wrong. It was wrong in the other
direction: a wall of fields where the two you came for were wherever they happened
to fall, and most of them — a prober's idle wait, how often the release API is
polled, how often one key's use is recorded — are values nobody has wanted to change
and that have a sane default in the code. Everything cut is **still read from the
environment and still overridable by a flag**, exactly as before; the table at the
bottom of this file is the list.

`GUARD_TOKEN` is the pointed absence. It was a row with a Generate button, and it
was a trap: generate one and the only thing that can read it back is the page it
just locked you out of. The token that protects the dashboard goes in the unit
file, where it has always been.

## The one rule

**Stored values are applied to the process environment at startup, and nothing
else changes.**

Everything above `internal/config` still reads `os.Getenv` or takes a flag,
sign-in still builds itself from `auth.FromEnv`, and a deployment that sets
everything in its unit file behaves exactly as it did before. There is no second
configuration system running beside the environment — there is one, and this
fills it in. Which is why the page is honest about needing a restart: a process
has its environment from the moment it starts, and only a start reads one.

## Two pages

The catalogue is drawn by two pages, and the group says which:

| page | groups |
|---|---|
| **Settings → Configuration** | access and ingest, cluster and loops, alerts, updates, the vault, paths and keys |
| **Settings → Security** | Google, Apple, sessions and admins |

A card per provider rather than one list of seven fields: somebody has a Google
console open, or an Apple developer account open, and never both.

Sign-in has a page of its own because it is a different kind of thing. The
configuration page is a long list somebody tunes — timeouts, intervals, a rate
limit. Those ten values decide *who may open the dashboard at all*, are set once
from a provider's console in a sitting, and are what somebody comes looking for
when they are locking an instance down; they were three quarters of the way down a
form about timeouts. Everything else that will ever be said about access — who is
on the members list, which sessions are open — belongs beside them.

It is one endpoint and one renderer: `GET /api/config` answers with the whole
catalogue, each group carries its page, and `config.js` draws the groups whose page
matches the one it is on. A second endpoint per page would be a second place for
the answer to be wrong. A group with no page named is drawn on the configuration
page, which is the right default — a new variable is a setting until somebody
decides it is a policy.

The **Restart Guard** button is the whole process's, not the page's: a value saved
on either page is a restart both can press, and the status line says so rather than
going quiet about a save somebody made next door.

## Precedence

```
an explicit flag  >  a stored value  >  the environment  >  the default
```

- **A flag typed on the command line wins**, because it is the escape hatch —
  the thing to reach for when the dashboard has stored something that will not
  start. `main` re-derives only the flags nobody passed (`flag.Visit` is how it
  knows the difference).
- **A stored value outranks the environment**, because otherwise the button
  would silently lose to a line in a unit file, which is worse than not having
  the button. The page says where each value came from — `stored here`, `from
  the environment`, `default` — so this is never a guess.
- **Clearing a field is a removal, not an empty value.** The row goes away and
  the environment is the fallback again. A stored empty string would shadow a
  name set in the unit file with nothing at all, which is how somebody turns
  sign-in off by resetting a field.

## Two values go in and do not come back

`GUARD_GOOGLE_CLIENT_SECRET` and the `GUARD_APPLE_PRIVATE_KEY` are **write-only**:
stored, applied, and never sent to a browser again. The row says `set` or `not
set`, the box is empty whatever is stored, and pasting a new value replaces it.

Guard is not where somebody looks up their Google client secret — the provider's
console is — and a value on screen is a value in a screenshot, a shared tab and a
support thread. The rest of that page is deliberately *not* treated this way: a
team id, an admin address or a base URL has to be readable to be worked with, and
masking those would be theatre. The two tokens guard issues itself are shown in
full for the opposite reason — the whole point of them is being pasted into a
collector.

Two consequences worth knowing:

- **An empty box means "leave it alone"** for these rows, not "remove it".
  Otherwise saving a neighbouring field would delete somebody's client secret.
- **Removing one is its own press.** `Remove` takes the value back out — and for a
  provider's credentials it takes the pair with it, because guard refuses to store
  half a configuration, so "remove the client secret" can only mean "turn Google
  off". Apple's private key is the exception: removing it alone is allowed when the
  key *file* is set, since that is where the key comes from then.

## What cannot be stored

`GUARD_DB_PATH` and `GUARD_SECRET_KEY` are what guard needs to open and decrypt
the database, so they cannot live inside it. Both are shown read-only — "where
is this configured" is a question the page should still answer — and the API
refuses them in words if asked anyway.

`GUARD_SECRET_KEY` is further **never sent to a browser at all**, unlike the two
tokens, which are the point of the Generate button below. The tokens are values
somebody pastes into a collector; the key is what makes every sealed row and
every backup readable, and it cannot be rotated without re-sealing all of them.
The page reports only that one is set.

Retention and the event cap are deliberately absent: they are rows in the
`settings` table, applied the moment they are saved rather than at the next
start, and they stay on the **Data storage** page. A second place to type them
would be a second answer.

## Saving

- **All or nothing.** A value that will not parse, a name guard does not know,
  or a provider's credentials left half-filled refuses the whole save, and
  nothing is written. Guard treats half a sign-in configuration as fatal at
  startup on purpose, so the moment to say so is while somebody is still looking
  at the field — not from a log file at the next restart, with the dashboard
  down.
- **Only what changed is sent.** Two people with the page open should not
  overwrite each other's untouched fields.
- **A field somebody is editing is never overwritten.** The rows are built once
  and patched afterwards; a row that has been typed in keeps what it holds.
- **Values are sealed at rest**, with the same keeper the SSH passwords and the
  stored secrets use. Half of this catalogue is a credential, and the database
  file gets copied to laptops and attached to bug reports.

## Restarting

Guard restarts by **exiting**. It runs unprivileged with `NoNewPrivileges` and
could not call `systemctl` if it wanted to; the unit's `Restart=always` starts it
again two seconds later, against the new environment. The button therefore
appears only under systemd — guard asks for `INVOCATION_ID`, which is the
supervisor's own word rather than a setting somebody has to keep true. Anywhere
else, exiting is just stopping, so the page says to restart the service by hand.

A row that has been saved but not started into says **saved · restart to
apply**, and the page says the same thing in one line at the bottom. Claiming a
rotation that has not happened is the one thing this page must not do: the
collector that matters is still presenting the old secret.

`guard-vault` reads the two settings that are its own (`GUARD_VAULT_ADDR`,
`GUARD_VAULT_TOUCH`) from the same table, the same way. It is a read like every
other one it makes — the vault's store has no method that changes a setting —
and it means the form is not quietly lying about half its rows. The two
processes are restarted separately, as always.

## In development: a `.env`

On a box the environment comes from the unit file. On a laptop there is no unit
file, and what a checkout already has is a `.env` — so **a development build reads
one at startup and writes it back when something is saved.** `make dev` runs in the
repo root, so the file lands beside the code, and everything else that reads a
`.env` — docker compose, a test script, direnv — sees the same values.

```
.env            read at startup, rewritten on every save
GUARD_DOTENV    name a different file, or set 0 to turn it off
```

Two rules, both conventional:

- **A real environment variable wins over the file**, so `GUARD_DB_PATH=x make dev`
  means what it says. Set-but-empty counts as unset, because that is how every
  other reader in guard treats it.
- **Lines guard does not own are left alone.** The guard variables are rewritten as
  one block under a header; a comment, a blank line, somebody's own `STRIPE_KEY`
  pass through untouched. Losing those to a settings save would be worse than not
  having the file.

The file is the *environment* layer, so a stored value still outranks it and the
page still says which is which — edit either. It is written 0600, because it holds
the operator token, and it is **off for a released build**: a binary on a server
that quietly wrote a `.env` into whatever directory systemd started it in would be
a surprise, and the box has a better place for all of this.

## When a stored value will not start

`GUARD_CONFIG_IGNORE=1` starts guard from the environment alone. It is the way
back from a value that stops guard from starting, which this page has to have an
answer for, given that the way you would otherwise fix it is the dashboard that
is not running:

```bash
sudo systemctl edit guard          # [Service] Environment=GUARD_CONFIG_IGNORE=1
sudo systemctl restart guard
```

Guard then runs on the unit file's values, the page says so in a banner, and
saving still works — nothing saved is used until the variable is removed. Fix
the field, drop the override, restart.

A single stored value that will not decrypt — the key changed, or the file came
from another instance — is skipped rather than fatal, for the same reason: a
guard that refused to start over one unreadable timeout is a guard that cannot
be fixed from the dashboard that stores it.

## Generating the two tokens

`GUARD_TOKEN` and `GUARD_OTEL_SECRET` are the only rows with a **Generate**
button, and that is a rule about what they are rather than a gap. Both are opaque
bearer tokens whose only property is being unguessable, so a random 32 bytes is
strictly better than something somebody typed — and the alternative is reaching
for a shell to run `openssl rand -hex 32`, which is the last step of this page
that still needed one.

Nothing else qualifies. An OAuth client secret is issued by Google; an alert token
is issued by whoever receives the alert; `GUARD_SECRET_KEY` *could* be minted and
must never be minted from a button, because changing it orphans every sealed row
in the database. A Generate beside any of those would produce a value the far end
has never heard of.

Pressing it stores the value like any other save — validated, logged, pending a
restart — and puts it on the clipboard, because the reason to generate one of
these is to paste it somewhere. Replacing a value that is already set asks first:
everything presenting the old one stops working at the restart. The two are
separate presses, because they are separate days — one is a laptop being lost, the
other a collector box being decommissioned, and rotating both because one was
asked for stops every exporter at once.

## Reading is admin, and the first visit says so

Every endpoint here is `admin`, reads included, and the values come back in the
clear. The alternative is forty masked fields nobody can check against the
provider's console they are copying from.

What that means in practice depends on how the instance is configured, and the page
says which:

| instance | these pages |
|---|---|
| no `GUARD_TOKEN`, nobody signing in | open — there is nothing here to leak, because nothing is set |
| `GUARD_TOKEN` set | the token is the whole of what protects the port, so it is asked for |
| sign-in configured | whoever is signed in, and on the members list as an admin |

On a token instance a fresh tab gets a 401, so the pages draw **an explanation and
a box to paste the token into** rather than an empty form: it lands in
`sessionStorage` — the same place the Admin token field on Data storage puts it — so
one paste unlocks every page in that tab and nothing is written to SQLite.

That is deliberately not relaxed for "nothing is configured yet". This page shows
`GUARD_TOKEN` in full, so opening it to unauthenticated callers on an instance that
has one would hand over the operator token, and with it every write endpoint and
ingest. The setup path for a fresh instance is the other way round: it starts open,
you configure it from here, and it closes behind you.

**If the token was generated here and you no longer have it**, nothing can read it
back — it is sealed in the database with the same keeper as the SSH passwords. Start
guard with `GUARD_CONFIG_IGNORE=1`, which runs it on the environment alone: nothing
asks for a token, and you can read or generate a new one. The panel says so, because
that is the one dead end this feature can create for itself.
