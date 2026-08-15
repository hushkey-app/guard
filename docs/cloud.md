# The cloud account

Guard stores one secret per cloud account: the provider's API key. Everything
that key unlocks is read live, on the page that shows it, and never copied into
SQLite — a plan, a power state and a bucket's endpoint are the provider's state,
and a copy of somebody else's state is only ever right by coincidence.

One key, three surfaces:

| | |
|---|---|
| **Registries** | `/registries` — the container registries the account owns, their repositories and tags |
| **Machines** | `/cluster` — the instance behind a declared machine: power, plan, transfer, snapshots |
| **Storage** | `/storage` — object storage subscriptions and buckets, their endpoints and their S3 keys |

It is one credential at the provider, so it is typed once, proved once and
revoked once. Add it under **Settings → Cloud accounts**. Removing an account
deletes nothing at the provider — and unlinks every machine that pointed into
it, because a link to a key guard can no longer open is a provider strip that
can only say "the stored key could not be opened".

The key is proved before it is stored (it has to answer the provider once) and
never comes back: the API answers `has_key`, the page draws dots. Rotation is
delete-and-add, so the proof cannot be skipped. It is sealed with AES-GCM from
`GUARD_SECRET_KEY`, exactly like the SSH passwords.

## More than one provider

There are two: **Vultr** and **Cloudflare**. They do not answer for the same
things, and the way that difference is handled is the whole design.

`internal/cloud` holds the vocabulary — a `Registry`, a `Storage`, a `Region` —
and splits the verbs into halves a provider may implement: `Registries`,
`RegistryMaker`, `Storages`, `StorageRenamer`, `StorageKeys`. Each provider
package implements as much as its API actually has. What a provider *can do* is
then derived by asking whether it implements the interface, not declared beside
it where the two could disagree:

| | Vultr | Cloudflare |
|---|---|---|
| Registries | yes | no — needs a Workers Paid plan, see below |
| Create / delete a registry | yes | no — the registry comes with the account |
| Object storage | yes, S3 subscriptions | yes, R2 buckets |
| Rename it | yes | no — a bucket is its name |
| Reveal / rotate S3 keys | yes | no — R2 keys are API tokens, minted at Cloudflare |
| Machines | yes | no |
| Needs an account id | no | yes |

`GET /api/cloud/providers` answers that table, and the dashboard draws itself
from it: the provider select, the word above the secret box, whether the form
asks for an account id, which accounts the machine link offers, and which
buttons a row gets. A button guard cannot honour is never drawn, and an
endpoint asked anyway answers in words — *"Cloudflare cannot hand out S3
credentials for its storage"* — rather than failing somewhere lower down.

Adding a third provider is a package that implements the halves it has, plus
one line in `server/apis/cloud`'s wiring and one id in `model.Providers`.

### Cloudflare needs two things

Every Cloudflare endpoint hangs off `/accounts/{account_id}`, so an account here
is a token **and** an account id. The id is not a secret — it is on the
account's overview page — and it is typed in rather than discovered, because a
token that can see two accounts would otherwise have guard guessing which one
was meant. It is stored in `provider_accounts.external_id`, in the clear,
beside the sealed key.

The token wants **Workers R2 Storage** read and write. Guard proves it by
listing buckets, which an account with no buckets still answers.

### Why Cloudflare has no registries yet

Cloudflare does run a managed container registry — `registry.cloudflare.com`,
one per account, addressed by account id — and it speaks the same OCI protocol
guard already uses for tags and manifests. Reaching it needs a Workers Paid
plan; without one the credentials call answers *"you do not have access to
Cloudflare Containers"*. So the half is not implemented, the account draws no
registry row, and adding it later means implementing `cloud.Registries` on that
one type. Nothing above it changes.

## Linking a machine

A machine in guard is *declared* — a name, an address, a health path, a
cadence — and that is deliberately independent of any provider. Most of the
machines worth watching are not in anybody's API, and they are watched the same
way.

Linking one to an instance adds the half a health check cannot see. A health
check says "the service did not answer". The provider says "the machine is
stopped". Those are different sentences, they call for different people, and at
three in the morning the difference is the whole answer.

The link is stored as an account id and an **instance id**, never an address: an
instance keeps its id across a reinstall and loses its IP to any number of
ordinary events, and a link that silently re-pointed at whoever holds the
address now would be worse than no link at all.

Link a machine by opening its row under **Settings → Cluster**. Or go the other
way with **Import**, which lists the instances an account runs that nothing here
watches yet and declares one in a click: the label becomes the name, the public
address becomes the address, the provider's tags become guard's.

An imported machine arrives **paused**. Guard has no idea what it serves or
where its health endpoint is — the address it can guess is `http://<ip>`, which
for most boxes answers nothing — and a machine that is red from the moment it
appears teaches people that red means nothing. No login is imported either:
there is nothing to import, and a stored login here is one that connected at
least once.

## What a linked machine can do

On `/cluster`, a linked machine grows a **cloud strip**: the power state, the
plan, the region, the addresses, and the month's transfer against the plan's
allowance. It is read when the page is opened and on an explicit refresh, never
on the dashboard's three-second tick — behind it is somebody's API rate limit
rather than guard's own SQLite, and a power state twenty seconds old has never
been what made an outage worse.

- **Start / Halt / Reboot.** The provider's switch, not the guest's. The stored
  command `sudo reboot` asks the operating system politely over SSH and needs
  the machine to be answering; this is the power cable, and it works on a box
  that stopped answering an hour ago. `halt` is a stop, not a delete: the
  instance keeps its disk, its address and its bill.
- **Snapshot.** An image of the machine's disk, taken while it keeps running.
- **Restore.** A snapshot written back over the disk. Everything on the machine
  that is not in the image is gone, the instance reboots into the restored disk,
  and the only undo is a snapshot taken before this one. It asks for the
  machine's name to be typed, the same confirmation locking and deleting take.

Nothing here destroys an instance. That is a thing to do in the provider's own
console, deliberately, once.

### Snapshots and who they belong to

Vultr's snapshot object carries a description and **no instance**, so "the
snapshots of this machine" is a question only guard can answer, and only about
snapshots guard took. That association — snapshot id to node — is the one
provider-adjacent thing stored here, in `cluster_snapshots`. Everything else
about the image (its size, its status, whether it still exists) is read live, so
a snapshot deleted in the provider's console simply stops appearing.

The rest of the account's snapshots are listed too, dimmer: one taken by hand an
hour before a bad deploy is exactly the one somebody will want, and hiding it
would send them to another website to find it.

## The three rules

The same three the stored commands keep, for the same reasons:

- **Every endpoint takes a node id, never an instance id.** The instance comes
  from the link, through `Store.ProviderTargetFor` — which is the only way to
  learn it. A caller cannot name a box, so a caller cannot aim a power switch or
  a rollback at one that is not on the row.
- **The lock is enforced in the store.** A locked machine still answers reads —
  it is still a machine somebody wants the power state of — and refuses
  everything that changes anything: restore, snapshot deletion, and the link
  itself. Locking says this machine's dangerous half is finished being
  configured, and a link is a new way to act on it.
- **Everything that changes anything is `admin`, and logged.** Node, instance,
  action, outcome. "Who power-cycled the database box" is a question asked after
  the fact, and the browser tab it was pressed in does not outlive the line.

## Both pages are lists

`/registries` and `/storage` are laid out the way `/cluster` is, and for the
same reason it was changed: a wall of cards is two to a line and mostly
whitespace, and finding the registry that is nearly full or the bucket on the
wrong endpoint means reading every card. A registry is a line and a bucket is a
line, grouped under the account key they came from — which is what they are
*under*, since two accounts on one provider are two bills and two logins.

The figures sit in fixed columns so they line up down the list, which is the
whole point of it. A registry row opens its repositories; a storage row folds
open, because what acts on a bucket — the credentials, Browse, Reveal, Rotate,
Delete — is not what somebody scanning the page came to read. An account whose
key stopped answering keeps its heading and says so there, rather than
disappearing from a page that would then look like it had no buckets.

## Registries, created and cancelled

`/registries` reads live and now also **orders**. **New registry** takes a name,
a region and a plan from the provider's own price list — read when the form is
opened, never baked in — and bills from the moment the provider accepts it,
which the dialog says in those words. **Public** means anybody can pull from it
without a credential; it is asked plainly rather than defaulted out of sight.

**Delete** on a registry row is the largest delete on these pages: every
repository, every tag, every artifact, and the subscription behind them. It asks
for the registry's name to be typed, the same confirmation locking a machine
takes, and it is logged whatever happens.

Both are offered only for accounts whose provider implements `RegistryMaker`. A
managed registry that arrives with the account is not something guard can order
or cancel, so a Cloudflare account is never in that select and the endpoints
refuse it in words.

## Object storage

`/storage` lists every object storage on every stored account, read live: Vultr
subscriptions and Cloudflare R2 buckets side by side. It creates and deletes
them everywhere, renames and rotates keys where the provider has such a thing,
and shows the S3 endpoint each answers on.

R2 is the narrower of the two. A bucket has a location *hint* rather than a
region — Cloudflare places the data near the first write and the hint only
nudges it — a storage class, and per-bucket usage, which guard shows because
Cloudflare reports it. A bucket cannot be renamed, because it *is* its name.
And there are no keys to reveal: R2's S3 credentials are API tokens hashed into
an access key, minted on Cloudflare's own token screen, and an account token
cannot mint another. So those rows carry no dots and no buttons — they say
where the credentials come from instead, which is true, unlike dots over a pair
that is never arriving.

The credentials, where a provider does hand them back, are the exception to
everything else in guard. Vultr returns the access key and the secret with
**every** read of a subscription — it is how the API is built — so:

- they live in unexported fields inside `internal/vultr` and come out only
  through `cloud.StorageKeys`, the one interface in the vocabulary that returns
  a secret — no listing type has a field one could land in;
- the listing carries neither: each row says a pair exists and draws dots;
- **Reveal** is its own endpoint, `admin`, which returns the pair once and
  writes a line saying it happened. Nothing is persisted.

That last one is deliberate rather than paranoid. Copying those two strings into
an application's configuration is the reason to have this page instead of
another tab on the provider's console; a page that only ever showed dots would
just mean the tab stays open.

**Rotate keys** issues a new pair immediately and invalidates the old one —
every deploy, backup job and uploader still holding the old secret fails from
that moment, which is the point of pressing it and why the dialog says so.
**Delete** destroys the subscription and everything in it; both ask for the
label to be typed.

## Looking inside a bucket

**Browse** opens a storage and walks it like a filesystem: folders, files,
sizes, dates, and a **Download** per object. It is the one read in guard that
does not go to a provider's API — objects are S3, so `internal/s3` signs
SigV4 requests against the storage's own hostname, from the server.

Three rules hold it in place:

- **It is read-only, by construction.** `cloud.StorageObjects` has three verbs —
  list the buckets, list one prefix, sign a link — and no verb that changes
  anything, so no endpoint above it can. Guard holding a credential that could
  delete somebody's objects would make a guard session a way to destroy their
  data; browsing is worth having, that is not.
- **The download is a signed link that expires**, five minutes out, minted on
  the press and logged with the object it names. The browser fetches the bytes
  straight from the storage, so a 40 GB object is never guard's problem, and a
  page full of live URLs is never sitting in the markup.
- **The folder view is the provider's work.** The listing asks for a delimiter,
  so a bucket with a million objects costs one page of rows rather than a
  million, and "load more" is the provider's own cursor.

Where the credential comes from differs, and that is the whole difference
between the two providers here. Vultr hands back a subscription's S3 pair on
every read, so nothing is stored: the pair is fetched at the moment it is used.
R2's pair cannot be minted by an account token, so guard stores one — sealed,
proved before it is stored by listing buckets over S3, optional. An R2 account
without one lists buckets, creates them and deletes them perfectly well, and
its rows say why they cannot be opened.

The pair is the one part of a stored account that is **edited in place**, from
**Add S3 keys** on the account row. Everything else about an account is
delete-and-add, so the proof that a key once worked can never be skipped; that
rule is kept here — the pair still has to read something before it is stored —
but the deletion is not, because the API key is what the account *is* and this
is a second credential for one feature. Requiring the account to be removed to
gain a Browse button would mean unlinking every machine that points into it.
Sending an empty pair forgets it, which turns browsing off again.

One shape follows from that. A Vultr storage is a *subscription* holding many
buckets, so it opens into buckets and then into prefixes; an R2 storage *is* a
bucket, so it opens straight into prefixes. `Containers` returning nothing is
what says which, and no page anywhere checks a provider name for it.

Uploading and deleting objects are deliberately absent, for the reason in the
first rule.

## What is stored

| | |
|---|---|
| `provider_accounts` | name, provider, the provider's own account id where one is needed, sealed API key. Renamed from `registry_accounts` on migration — same rows, same key |
| `cluster_nodes.provider*` | the link: provider, account id, instance id |
| `cluster_snapshots` | snapshot id → node id, so a machine can say which images are its own |

Nothing else. Registries, repositories, tags, instances, snapshot state, storage
subscriptions and every credential the provider hands back are read on demand
and kept nowhere.
