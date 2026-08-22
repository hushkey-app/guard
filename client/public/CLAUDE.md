# client/public — the dashboard's JavaScript

**Scoped gate:** `make css && make test`
(`make test` runs `node client/public/store_test.mjs` and `modules_test.mjs`.)

**Two build steps here silently do nothing when skipped.**

1. **A new `*.js` file needs its own `@source` line in the Makefile's `css`
   target.** Tailwind only emits classes it finds in the `@source` globs, so
   without that line every class the module names is simply absent from
   `client/public/app.css`.
2. **`make css` must run before `make`**, not after — the binary embeds
   `client/public`, so a stylesheet rebuilt after the Go build is one the
   server does not serve. It needs Node and network; it is the only target that
   does.

A class assembled from a variable (`` `col-span-${n}` ``) is **never** emitted.
Those go in inline styles.

`store.js` is the data path and lives outside the outlet, so it is evaluated
once per session. Every page goes through `ensure`/`set`/`subscribe`; a page
that fetches on its own is the one still saying "Loading…" on the way back.
A background refresh that changes nothing must redraw nothing, or it eats a
half-typed threshold and the scroll position.

Panels are cloned from `<template>` elements in `client/ui/components`, so the
chrome stays real shadcn markup and every class stays where Tailwind looks.

Files under `internal/ingest/` (the tracker) are **not** dashboard JavaScript
and get no `@source` line.
