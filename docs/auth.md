# Signing in

Guard's front door: sign in with Google or with Apple, and nothing else.

Everything here is off unless the environment configures it. With no OAuth
credentials guard behaves exactly as it always has — open, or behind
`GUARD_TOKEN` — because the instance most people run is a container on a laptop,
and a login screen it cannot get past would be a downgrade rather than a
feature.

```
internal/auth/auth.go       the configuration, the service, what is enabled
internal/auth/guard.go      the middleware, and the API's permission answer
internal/auth/handlers.go   /auth/{provider}/start, the callbacks, sign out
internal/auth/oidc.go       one OpenID Connect provider: authorize, exchange
internal/auth/google.go     endpoints, scope, the pasted client secret
internal/auth/apple.go      endpoints, form_post, the client secret guard signs
internal/auth/jwt.go        id token verification and the provider's key set
internal/auth/session.go    the cookie, the origin, the "where was I" parameter
internal/telemetry/auth.go  sessions and sign-ins in flight
internal/telemetry/members.go  the allowlist
client/pages/login/         the login page — a .raw route, its own document
client/pages/settings/members/  the members page
```

## The three rules

**Configured is on, unconfigured is off.** Give guard Google credentials and the
Google button appears; give it Apple's and Apple's does; give it both and there
are two. The login page is drawn from the providers that could actually be
built, so a button that cannot work is never on screen. Half a configuration —
a client id with no secret — is fatal at startup rather than ignored, because
somebody who set one and forgot the other meant to close the door.

**The provider says who you are; the members list says whether you may come
in.** An OAuth provider will happily prove that a complete stranger owns their
own Google account. That is identity, not authorization. The allowlist is the
`auth_members` table plus whatever `GUARD_ADMIN_EMAIL` names, and it is the only
thing that decides.

**The session is guard's, not the provider's.** Guard asks for an identity token
once, verifies it, and issues its own cookie. It keeps no access token and asks
for no refresh token, so nothing stored here can read anything from Google or
Apple afterwards. The database holds a SHA-256 of each cookie, never the cookie:
a stolen copy of `guard.db` proves who signed in and grants nothing.

## Configuration

| Variable | Meaning |
| --- | --- |
| `GUARD_GOOGLE_CLIENT_ID` | OAuth client ID, type "Web application" |
| `GUARD_GOOGLE_CLIENT_SECRET` | its secret |
| `GUARD_APPLE_CLIENT_ID` | the **Services ID**, not the App ID |
| `GUARD_APPLE_TEAM_ID` | the team the Services ID belongs to |
| `GUARD_APPLE_KEY_ID` | the Sign in with Apple key's id |
| `GUARD_APPLE_PRIVATE_KEY` | the contents of the `.p8` file |
| `GUARD_APPLE_PRIVATE_KEY_FILE` | or a path to it, which is easier to deploy |
| `GUARD_ADMIN_EMAIL` | one address, or several separated by commas |
| `GUARD_AUTH_BASE_URL` | `https://guard.example.com` — set this behind a proxy |
| `GUARD_AUTH_SESSION_TTL` | a Go duration; `168h` (seven days) by default |

The redirect URI registered at the provider is

```
<base URL>/auth/google/callback
<base URL>/auth/apple/callback
```

and it is compared as a string at both providers: scheme, host, port, path, no
trailing slash. Without `GUARD_AUTH_BASE_URL` guard derives the origin from the
request, honouring `X-Forwarded-Proto` and `X-Forwarded-Host`. That works on a
laptop and is worth pinning in a deployment — the worst a forged header can do
is send somebody to a redirect URI the provider refuses, but "the provider
refuses" is a poor error message compared to a configured string.

### Google, in the console

An **OAuth client ID** of type Web application. Its Authorised redirect URI is
the callback above. Guard asks for `openid email profile` and adds
`prompt=select_account`, because a dashboard is exactly the sort of thing
somebody opens from a browser already signed in to the wrong Google account.

### Apple, in the portal

A **Services ID** (the App ID will not work), whose Return URL is the callback
above, and a **Sign in with Apple key** whose `.p8` you download once.

Three things about Apple are not optional and are handled rather than worked
around:

- **There is no client secret.** What Apple calls one is a JWT signed with that
  P-256 key, so guard mints a fresh one — valid thirty minutes — per exchange.
  A secret that lives no longer than the request it was made for cannot be
  replayed out of a log.
- **The callback is a cross-site form POST**, because any requested scope forces
  `response_mode=form_post`. This is why the sign-in state lives in SQLite: a
  `SameSite=Lax` cookie is not sent on a cross-site POST, and a cookie that has
  to be `SameSite=None` to work is a cookie that is sent everywhere.
- **The name arrives exactly once**, in a `user` form field on the very first
  authorization, and never again — not even in the id token. Guard reads it only
  when the token carried no name, so a forged form field cannot overwrite what
  the provider signed for.

Apple's "Hide My Email" gives a `@privaterelay.appleid.com` address. That relay
is what the provider will report, so that relay is what has to be on the members
list.

## The flow

1. `GET /auth/{provider}/start` mints a random state and nonce, writes them to
   `auth_states` with the redirect URI and where the visitor was heading, and
   redirects to the provider.
2. The provider comes back to `/auth/{provider}/callback` — a redirect with a
   query for Google, a form POST for Apple. Both methods reach the same handler,
   because the difference is only where the parameters live.
3. The state is **claimed**: read and deleted in one transaction. Replay it and
   there is nothing there, which is what makes checking it worth anything. An
   unknown state is refused before any code is exchanged.
4. The code is exchanged for an id token, and the token is verified: signature
   against the provider's published key set (RS256 or ES256 only — never an
   algorithm read from the token), issuer, audience, expiry, and the nonce this
   particular sign-in generated. The nonce is the one that catches the
   interesting attack: without it, a token minted for another session of the
   same application is a valid login here.
5. The address is looked up on the members list. If it is there, guard issues
   its own session cookie and the browser lands on the page it was originally
   asking for.

Everything that can go wrong ends at `/login?error=<code>` with one of six fixed
sentences. The provider's own words go to the log, never to the page — a login
screen that prints whatever `?error=` says is a phishing surface.

## Who may do what

Two roles, and the line between them is the only one guard has ever needed:

- a **member** reads everything the dashboard shows;
- an **admin** can also change things — retention, machines, cloud accounts, and
  the members list itself.

Endpoints already declared `Roles: []string{"admin"}` for every write, so with
sign-in on that declaration starts meaning "an admin", and with sign-in off it
keeps meaning what it did: `GUARD_TOKEN`, or a warning that write endpoints are
open.

`GUARD_ADMIN_EMAIL` is checked beside the table rather than seeded into it. It
is always an admin, it is listed on the members page but cannot be edited there,
and it is the way back in when the last stored admin removes themselves. An
instance with an empty database still has somebody who can sign in.

The member is looked up **per request** rather than trusted from the session
row, which is what makes a removal take effect immediately: the cookie still
exists, the row it points at still exists, and the next request finds the person
is no longer on the list — so the session is deleted from under them. Removing
somebody through the page also deletes every session they hold, so an open
dashboard stops working on its next request rather than at the end of the week.

## What stays open

The middleware lets four things past without a session, and one of them matters
enormously:

- `/v1/…` — the OTLP receiver and the browser intake. An exporter holds a bearer
  token, not a cookie, and cannot sign in with Google; putting a login screen in
  front of `/v1/logs` would break every collector pointed at guard on the day
  sign-in was switched on. These have their own guards: `GUARD_TOKEN` and, for
  the browser door, the origin allowlist.
- `/login`, `/auth/…` — the flow itself.
- `/static/…` — the stylesheet and the scripts, which are the same bytes for
  everybody.
- `/healthz` — an orchestrator's probe is not a person.

Anything else needs a session, or `Authorization: Bearer $GUARD_TOKEN`, which is
how the seed tool and anything scripted keep working.

A browser navigating without a session gets the login page with `?next=` set. A
JSON caller gets `401` and `{"error":…,"login":"/login"}` — never a redirect
into an HTML form, which a `fetch` would follow and paste into the dashboard.
`core.js` watches for that 401 and sends the tab to the login page, so a session
that expires mid-shift surfaces as a login screen rather than as seven panels
failing at once.

## The two tables

```sql
auth_sessions(token_hash PK, provider, subject, email, name, picture,
              created_at_ns, expires_at_ns)
auth_states(state PK, provider, nonce, redirect_uri, next, expires_at_ns)
auth_members(email PK, role, name, provider, added_by, added_at_ns, last_seen_ns)
```

Neither of the first two holds a secret. Sessions are keyed by hash; a state is
worthless once claimed, which happens the first time it is presented. Expired
rows are collected on the way past — reading an expired session deletes it, and
starting a sign-in sweeps the states nobody finished — so nothing here grows
without bound on an instance people keep closing tabs on.

Addresses are stored and compared lowercased. They are technically
case-sensitive to the left of the `@` and no provider treats them that way;
matching case-sensitively would only ever produce a member who cannot sign in
because of how they typed their own address.

## Testing it

`internal/auth/auth_test.go` runs the whole flow against a mock identity
provider: an `httptest` server that publishes a JWKS, mints id tokens signed
with a key generated for the test, and checks the client credentials guard
sends it — including verifying that Apple's client secret is a correctly signed
ES256 JWT addressed to `https://appleid.apple.com`.

Mocked at the provider rather than at guard's own seams, on purpose. What is
worth testing is not that a function was called: it is that the exchange sends
what Google and Apple require, that a token guard did not ask for is refused
(wrong nonce, wrong audience, wrong issuer, expired, unverified address), that a
state is good for exactly one use, and that a browser ends up with a cookie that
opens the dashboard and stops working the moment its owner is taken off the
list.
