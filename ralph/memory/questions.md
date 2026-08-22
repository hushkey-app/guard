# Questions for a person

Anything an iteration could not decide without guessing. One bullet each:
the task id, the question, and what you did instead (built it per the spec,
skipped the task, chose a default).

Nobody is reading this while the loop runs. It is the handover.

## Open

- **A5** — the plan gives `Store.PreviewPathRules([]string) []string`, which can
  only apply the rules already stored; C3 asks the preview to prove a rule
  "before it is stored", and CLAUDE.md's `.env` import sets the rule that the
  dry run and the write are the same call. Built it as
  `PreviewPathRules(rules []model.PathRule, paths []string) ([]string, error)`
  — the same `preparePathRules` the save runs, so the dialog cannot describe
  something the press will not do. The error is how a pattern that will not
  compile is reported.

- **A6** — the strip's session count has no source but `analytics_seen`: the
  rollup is keyed per path, so summing it counts one visit once per page and
  views-per-session would read ~1.0 forever. A7 purges seen behind the rollup
  (7 days against 90), so on those defaults the strip goes quiet for any window
  older than a week while Views stays complete — the ratios would read as
  nothing rather than as a number, which is the silence guard prefers but is
  still a strip that cannot answer "versus last month". Built it per the plan
  and said so in the code. The two ways out, for A7/C1/D5 to choose between:
  hold the seen window at the rollup window (the table that grows with traffic
  grows thirteen times), or write one site-wide row per day at ingest (a
  sentinel path like `(other)`, which the grid read and the backup then have to
  know about).

- **A7** — the spec (§5) says the two settings are "analytics rollup days,
  analytics raw hours" while the same section says the raw feed is swept by the
  telemetry retention, and the plan says rollup days plus **seen** days. Built
  the plan's: raw follows `retention_hours` (one answer to "how long is raw
  telemetry kept"), rollup 90 days, seen 7. A raw-hours row would be a second
  number for a table nobody reads twice.
- **A7** — `UpdateSettings` takes the whole `model.Settings`, and the storage
  form posts only the two numbers it knows about, so a zero would have been a
  retention of none. Treated an omitted analytics window as "unchanged" and said
  so in the code; if E5 is meant to make them required, that is the line to
  delete.
- **A7** — A6's question (the strip can only count as far back as
  `analytics_seen`) is now a number somebody types rather than a constant: seen
  may be raised as far as the rollup window and is refused past it. Neither of
  A6's two ways out was built.

- **C2** — the spec says a deleted action "stops being counted", but discovery
  is automatic and `analytics_actions` has no blocklist column, so the next
  beacon carrying the name discovers it again. Built delete as a purge — the
  actions row, the rollup rows, the seen rows and the raw feed, in one
  transaction — and said so in the method's comment. If it is meant to be a
  mute, that is a column in A2's schema and a check in `AddAnalytics`, not
  something the endpoint can do.

