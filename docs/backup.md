# Backup

Guard's configuration is a lot of typing: the machines and where they answer,
the commands kept for each one, the environments injected onto them, the cloud
accounts, the saved views, the alert rules and where they deliver, the secrets
an application boots with, who may sign in, and the stored settings. All of it
lives in one SQLite file, which is easy to copy and easy to be wrong about —
copy it and you take four hundred megabytes of telemetry with it, and you take
the ciphertext of every credential without the key that opens it.

**Settings → Backup** is the other way: one JSON file with the configuration and
nothing else, and a restore that puts it back — on this instance, or on a box
that has never seen it. Two cards and one line of counts, because an export is
all of the configuration or none of it: the page listed every section with its
rows once, and a list nobody can act on reads like a chooser that does not
exist.

## What travels

The catalogue is `backupTables` in `internal/telemetry/backup.go`, and it is the
whole feature. A table in it travels; a table not in it never does.

| Section | Tables |
| --- | --- |
| Storage | `settings` |
| Configuration | `config` |
| Access | `auth_members` |
| Alerts | `webhooks`, `cluster_monitors` |
| Cloud | `provider_accounts` |
| Cluster | `cluster_nodes`, `cluster_actions`, `cluster_assignments`, `cluster_snapshots`, `cluster_env`, `cluster_env_state` |
| Views | `views` |
| Secrets | `secret_workspaces`, `secret_envs`, `secrets`, `secret_keys` |

And what is deliberately left behind: `events`, `event_totals`, `event_instances`,
`metadata`, `cluster_checks`, `cluster_runs`, `cluster_stats`,
`cluster_monitor_state`, `secret_reads`, `auth_sessions`, `auth_states`.

The first four are the telemetry and its counters — the part that grows without
bound and the part the next minute of ingest reproduces. The rest is history and
live state: what a machine's disk was at 9am, which command ran last night, who
is signed in right now. None of it describes how guard is configured, and a
backup carrying it would be a database copy wearing a different extension.

`secret_keys` does travel, hashes and all, so **a vault token keeps working
after a restore**. That is the point of restoring onto a new box: the
applications reading their secrets are not the ones being migrated.

## Columns are matched by name, at both ends

The exporter asks the database what columns the table has; the importer asks its
own; only the names in both are written, and the rest are reported.

So a backup taken by an older guard restores into a newer one with the new
columns left at their defaults, and a column that has since been dropped is a
line in the report rather than a failed restore. Nothing in the export path has
a hardcoded column list to forget to update — which matters, because the tables
here grow a column every few releases and the first hardcoded list to fall
behind would silently drop the thing that had just been added.

## The credentials travel; the passphrase decides how

Ciphertext in guard's database is bound to that database's key
(`GUARD_SECRET_KEY`, or `<db>.key` beside the file). Copying those bytes into a
backup would be worth nothing anywhere — not even on the machine they came from,
since the key belongs to the database file rather than to the rows in it. So the
export **opens** every sealed value with this instance's key, and the import
**seals** it again with whatever key the receiving instance has. What the
passphrase changes is what the value looks like in between:

- **A passphrase.** The value is sealed under a key derived from it — scrypt,
  N=2¹⁵, a random salt written into the file. The file goes anywhere and is
  worth nothing to anyone without the words.
- **No passphrase.** The value goes in as itself. **The file is the
  credentials**: every SSH password, provider key, webhook token, machine
  environment, stored setting and secret is readable by anyone holding it. That
  is the only way a restore comes back as the instance that was backed up, so it
  is the default — and the page says so where the box is.

A known phrase is sealed into the header of a passphrase file, so **the wrong
passphrase is refused before anything is read**, let alone written. Guard cannot
check a passphrase against anything but the file: it stores nothing about it,
and there is no recovery.

The importer decides per value, from what the cell actually holds rather than
from the header: a text cell is the value, a blob is sealed. So there is no mode
to keep in step, and a file whose header disagrees with its body still lands
correctly.

`"secrets": "omitted"` is the first version of this format, which left the
credentials out. Those files still restore, with every credential blank — which
is what they carry. It was the wrong default: a restored instance whose logins
and API keys are all empty answers "no stored key" on every page, weeks after
anyone remembers taking the backup.

## A restore replaces

Every section is emptied and rewritten inside one transaction, **ids and all**,
because the point of a backup is that the instance afterwards is the instance
the file describes. A merge would have to answer "which machine is id 3 on this
box", and it would answer it wrongly the first time two instances both had one.

What that means in practice:

- What is on this instance and not in the file is **gone** — a machine added
  yesterday, a rule somebody wrote this morning.
- The dashboard asks for `replace` to be typed, and names what it is about to
  overwrite: how many sections, how many rows, when the file was taken and by
  which build.
- Everything that can fail happens **before** the transaction: the format check,
  the passphrase, and every re-seal. A refusal leaves the instance exactly as it
  was found.
- Foreign keys are deferred to the commit, so the order inside the transaction
  is guard's to choose and the answer is still checked at the end.
- A file whose `format` is newer than this build reads is refused rather than
  partly applied.

## What a restore resets on purpose

**Alert state is cleared** — `cluster_monitor_state` is emptied and the view
alerts' firing columns are zeroed. What a rule has already told a receiver is
about the machines this instance was watching a moment ago, not the ones it is
watching now. Starting from "nothing has been reported" means the next pass
fires on what it can actually measure, and no receiver is closed out of an
incident guard never observed. It is the same reasoning as editing a rule.

**Stored settings need a restart.** A process has its environment from the
moment it starts, so a restored `config` row is a row until guard is restarted —
the report says so and the page offers the same restart the configuration page
does, which is guard exiting and its supervisor bringing it back.

**Open sessions are untouched**, because `auth_sessions` is not in the
catalogue. Restoring a members list can still lock somebody out on their next
request; `GUARD_ADMIN_EMAIL` is the way back, as it always is.

## The file

```json
{
  "format": 1,
  "guard_version": "v0.3.0",
  "created_ns": 1787166213459459000,
  "secrets": "passphrase",
  "kdf": { "algorithm": "scrypt", "salt": "…", "n": 32768, "r": 8, "p": 1, "check": "…" },
  "tables": [
    { "name": "cluster_nodes", "label": "Machines", "group": "Cluster",
      "columns": ["id", "name", "url"],
      "rows": [[1, "VPS-1", "http://10.0.0.5:8000"]] }
  ]
}
```

Columns are named once and rows are arrays, so a backup of a thousand secrets
does not repeat every column name a thousand times. Each cell carries its SQLite
storage class through JSON, which is two bugs avoided rather than a preference:
a nanosecond timestamp does not survive a JSON number decoded as a `float64`, so
integers are parsed from their literal text; and a BLOB decoded into `any` comes
back as a base64 *string*, indistinguishable from a text column holding base64,
so blobs are written as `{"b64": "…"}` and stay blobs.

## The endpoints

All three are `admin`, reads included — the summary is only counts, but the
export is every credential guard holds.

| Method | Path | What |
| --- | --- | --- |
| `GET` | `/api/backup` | What a backup taken now would hold, section by section, and what it leaves behind |
| `POST` | `/api/backup` | The file, given `{"passphrase": "…"}` (empty is a real answer) |
| `POST` | `/api/backup/restore` | `{"passphrase": "…", "backup": {…}}` — replaces, and reports |

The export is a POST rather than a link, because a link would have to carry the
passphrase in a URL, where it would land in the access log of every proxy in
front of guard. The browser turns the answer into a download; guard keeps no
copy, because there is nowhere for one to live that is not the disk this is a
backup of.
