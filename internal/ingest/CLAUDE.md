# internal/ingest — the doors

**Scoped gate:** `go test ./internal/ingest/`
Tracker script too, when touched: `node --check internal/ingest/track.js`.

Two doors, and the difference is the whole design:

- `/v1/{logs,traces,metrics}` — a process someone deployed, holding a token
  they issued. `GUARD_OTEL_SECRET` or `GUARD_TOKEN`, 16 MB, identity from the
  payload.
- `/v1/rum/*` — a **stranger**. A browser holds no secret, so: off unless
  `GUARD_RUM_ORIGINS` is set, 256 KB, 120 requests a minute per address, and
  **the service identity is assigned by guard, never accepted from the
  payload**. Without that last one a visitor can post spans claiming to be your
  API, and every panel on the dashboard is something a stranger can write to.

A new browser path is a path on the **existing** door — mounted from
`RegisterBrowser`, sharing the origin check, the limiter and the body budget.
It adds no configuration of its own.

No `Origin` header at all is allowed on purpose: that is a server-to-server
relay, which is the recommended deployment (`docs/browser-telemetry.md`).

Analytics work: `ralph/specs/analytics.md` §6, plan tasks B1–B3.

`navigator.sendBeacon` posts `text/plain` — a CORS-safelisted type, which is
what keeps the flush on a closing tab free of a preflight it would never live
long enough to complete. So the analytics door checks no Content-Type, and a
new browser path must not grow one.
