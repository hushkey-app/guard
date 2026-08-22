---
name: guard-ui
description: Build or change Guard's dashboard UI — pages, layout, sidebar, shared components, icons, modals, drawers, wizards, Tailwind classes, shadcn-templ usage. Read this BEFORE editing anything under client/pages/ or client/ui/, adding a class to a .templ file, or building any overlay. Covers what belongs in the shell vs the layout vs a page (getting this wrong produces chrome that freezes on the first page loaded), how to build a modal with no JavaScript, why a CLOSED panel still costs full render work every tick unless you gate it, the icon registry, the aria-current rule, and the two build steps that silently do nothing if skipped.
---

# Guard UI

Go + templ + Tailwind v4 + shadcn-templ v2. No React, no bundler, no client
framework. `CLAUDE.md` covers the product; this covers the interface.

## Read first, in this order

1. `docs/shadcn-templ.md` — the offline digest of the **exact beta pinned here**.
   Do not guess React shadcn APIs and do not fetch the website. The Go API is
   different.
2. `howl_conventions` (MCP) section `"Routing conventions"` or
   `"Making a page interactive"` — the file-naming rules are not guessable,
   because Go rejects `_layout.templ` and `[id].templ`.

## Where a thing goes

This is the decision that goes wrong most, and it fails silently.

A client-side navigation swaps **`#outlet` and nothing else**. The document
shell is rendered once, by the server, on a cold load.

| put it in | when | file |
|---|---|---|
| **the page** | it is this route's content | `client/pages/<path>/index.client.templ` |
| **the layout** | it is chrome, and it depends on the route | `client/pages/layout.templ` |
| **the shell** | it must outlive a navigation and does NOT depend on the route | `client/pages/app.templ` |
| **shared** | two pages render it | `client/ui/ui.templ`, or `client/ui/components/` for a big one |

**Route-dependent markup in the shell freezes** on whichever page was loaded
first, and only a refresh corrects it. That is why the sidebar lives in
`layout.templ`. The shell keeps only the detail drawer, the tooltip and
`DrillRow` — furniture that is deliberately outside the outlet because it must
survive a navigation.

The exceptions that legitimately stay in the shell are maintained by the client
runtime: `aria-current` on every same-origin link is re-applied after each
navigation, and `document` fires `howl:navigate` for anything else.

## Modifiers

- `index.client.templ` — server-rendered and browser-rendered. The default here.
- `*.raw.templ` — **its own document**: no shell, no layout, no sidebar. `/login`
  and `/status`. The client hands these to the browser rather than swapping them
  into the outlet, so a link to one is a full page load by design.
- `*.bare.templ` — keeps the shell, drops the layout chain.
- `id.dyn/` — a `{id}` path parameter. Read it with `router.Param(ctx, "id")`.

A raw route must never appear in the sidebar; `ui.NavGroups` drops them
automatically, so no list needs editing.

## Icons

One registry, not one templ per icon: `client/ui/icons.go`.

```templ
@ui.Icon("logs", "size-4")
```

Adding one is a row in `Icons`: the inner paths, plus only the attributes that
differ from the default (24×24, `stroke="currentColor"`, width 1.5, no fill). An
unknown name renders nothing — a missing icon should cost a missing icon.

Icons are `currentColor`, so they inherit the row's colour. Size them with the
class argument, never with a `width` attribute.

## The sidebar

`client/ui/nav.go` owns the order and the grouping; the route table owns the
labels, so renaming a page renames its row.

- `navOrder` — groups, each with a heading (`Watch`, `Signals`,
  `Infrastructure`). The heading is the divider: at eight rows a word separates
  the groups better than a rule does.
- `navIcons` — pattern → icon name. No entry falls back to `dot`.
- A route in the table but not in `navOrder` joins the last group rather than
  vanishing. Dropping off the sidebar is the worse failure: the page still
  exists and is simply unreachable.

`ui.NavLink` marks the active row with `aria-current` and styles from it —
never with a class computed at render time. The active rail is a
`before:` pseudo-element so it cannot shift the icon and label by a pixel when
it appears. `router.Under`, not `==`, so `/logs` stays lit on `/logs/42`.

## Modals, drawers, disclosures

Guard serves **no interactive shadcn bundle**, so dialog/popover/select are not
available. Every overlay here is native:

| want | use | example |
|---|---|---|
| explanation, inline | `@ui.Note("title") { … }` — a `<details>` | the notes on `/cluster` |
| modal / drawer | checkbox + `peer-checked:` sibling selector | `components.MachineDialog`, the nav drawer |

No JavaScript, keyboard-operable, and nothing to re-hydrate after a navigation.
A backdrop `<label for="…">` closes it. Do not put `backdrop-filter` on a
full-viewport backdrop: the live dot behind it keeps it re-compositing.

### Gate whatever fills a closed panel

This is the one that has already cost us. `guard.js` and `cluster.js` find their
work by asking whether the markup is present — `if (qs("[data-cluster-rows]"))`.
A closed panel is `display:none`, and **`display:none` markup is still present**,
so it keeps answering yes.

Moving the machine list into `MachineDialog` meant `render()` rebuilt one large
row per machine every three seconds into a dialog nobody had opened. It read as
the whole page being slow, because it was.

```js
const MACHINES_PANEL = "machine-dialog";
function machinesPanelOpen() {
  const toggle = document.getElementById(MACHINES_PANEL);
  return !toggle || toggle.checked;   // no panel => the rows ARE the page
}

if (machinesPanelOpen()) { …rebuild the rows… }

document.addEventListener("change", (event) => {          // or it opens onto stale content
  if (event.target?.id === MACHINES_PANEL && event.target.checked) render();
});
```

Applies to anything conditionally hidden, not just modals: a tab that is not the
current tab, a shut `<details>`, an off-canvas drawer. All of them are present to
every `querySelector` in the app.

### Wizards

Steps are radios plus sibling selectors — no JavaScript, and every `[data-…]`
input stays in the DOM on every step, so the submit handler reads the form it
always did. Two rules:

- **Split by meaning, not by length.** `ClusterForm` is seven fields; it is two
  steps because step two is optional and step one is not.
- **Keep `required` fields on the first step**, and the submit button outside the
  steps. A `required` field in a hidden step is one the browser refuses to focus
  and refuses to explain.

## Tailwind: the two things that silently do nothing

1. **`client/public/app.css` is a build artifact and is committed.** Tailwind
   only emits classes it can *find*. Use a class nothing used before and you
   must run `make css` — the one target that needs Node. Verify it landed:
   `grep -o '\.your-class' client/public/app.css`.
2. **A class assembled from a variable is never emitted at all.**
   `` `col-span-${n}` `` produces nothing. Use an inline style, or enumerate the
   variants literally.

A new file under `client/public/*.js` needs its own `@source` line in the `css`
target or nothing in it is scanned.

Load-bearing: `<html class="dark style-nova">`. `style-nova.css` nests every
`cn-*` rule under `.style-nova`, so dropping it unstyles every shadcn component
at once.

## JavaScript

`guard.js` binds by **delegation on `document`** (`event.target.closest("[data-…]")`),
which is why its controls keep working after the sidebar moved inside the
outlet and is re-rendered on every navigation. Keep it that way: a listener
bound to a specific element in the outlet dies on the next navigation.

Guard uses **static** shadcn components only. Interactive ones (select, dialog,
popover) need an upstream JS bundle the direct-import workflow does not serve;
filter controls are native elements wearing `cn-native-select` or the Input
component, because `guard.js` drives them through `.value`.

For state that must react in the shell, listen for `howl:navigate` on
`document` — nothing outside `#outlet` is re-rendered.

## Before you finish

```bash
make            # fsroutes -> templ generate -> build (in that order, always)
make css        # only if you used a class that did not exist before
go run github.com/mirairoad/howl-go/core/cmd/howl check
```

`howl check` reports the browser-side mistakes that fail silently: a `Mount`
that subscribes with no `Unmount`, a discarded `dom.On` release func, a
`@pkg.Component()` the package does not declare.

## House style

Comments explain **why**, not what: the reason a line exists, the bug it
prevents, the measurement behind a number. Match the density of the file you are
editing. When a claim is measurable, measure it and put the number in the text.
