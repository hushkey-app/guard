# client/pages — howl-go filesystem routes

**Scoped gate:**
`go run github.com/mirairoad/howl-go/core/cmd/howl check && make test`

**Read `../howl-go/llms.txt` before touching anything here** (the `replace` in
`go.mod` points at it), or call the `howl_conventions` MCP tool. Go's toolchain
rejects `_layout.templ` and `[id].templ`, so none of these names are guessable.

- `index.templ` → a route. `index.client.templ` also renders through
  `client/public/views.wasm` — the default here.
- `app.templ` is the document shell, rendered **once** by the server on a cold
  load. `layout.templ` wraps its directory. `id.dyn/` is a `{id}` parameter,
  read with `router.Param(ctx, "id")`. All reserved names.
- `*.raw.templ` is its own document — no shell, no sidebar, a full page load.
- Pages import `core/router`, **never** `core/app`, take no arguments, and read
  everything from `ctx`.
- `fsroutes_gen.go` and every `*_templ.go` are generated. Edit the `.templ`.

**A client-side navigation swaps `#outlet` and nothing else.** Route-dependent
markup put in the shell freezes on whichever page loaded first and only a
refresh corrects it. That is why the sidebar lives in `layout.templ`.

Then read `.claude/skills/guard-ui/SKILL.md` and `docs/shadcn-templ.md`.
