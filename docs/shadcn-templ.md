# shadcn-templ offline agent guide

Guard pins `github.com/axadrn/shadcn-templ/v2@v2.0.0-beta.3` and its whole dashboard is built from it. Read this before changing the UI. It summarizes the upstream documentation and Guard-specific integration so routine work does not require fetching templui.io or shadcn-templ.com. A verbatim snapshot of the pinned core guides and all 52 component pages lives in `docs/shadcn-templ-upstream/`.

## What it is

shadcn-templ is the Go/templ pendant of shadcn/ui: accessible templ components, Tailwind CSS styling, and vanilla JavaScript for interactive behavior. It is MIT licensed. Version 2 is beta; pin exact versions and expect API changes.

The supported workflow copies component source into the application with the CLI. The direct Go import workflow Guard uses is explicitly experimental upstream. It works for static components; see "Interactive components" for the part that does not.

## Guard integration

- Every page imports shadcn-templ packages directly. DaisyUI is gone.
- `client/styles/app.css` is the stylesheet source: Tailwind, the three upstream imports, Guard's theme tokens on `:root`, and a short unlayered block for the detail panel and `.signal-dot`.
- `make css` compiles it to `client/public/app.css`, which is committed, so `make`, `make dev` and `go test` need no Node/npm. It also writes the gitignored `client/styles/app.sources.css`, which carries this machine's module-cache path.
- Tailwind only emits classes it finds in the `@source` globs — `client/pages/**/*.templ`, `client/ui/**/*.templ`, and `client/public/guard.js`. **A class used only in a dynamically built string in guard.js must be written out in full**, or it will not exist in the bundle.
- `<html class="dark style-nova">`. `style-nova.css` nests every `cn-*` rule under `.style-nova`; without that class every component renders unstyled but structurally correct, which is a confusing failure. `dark` is what the `dark:` variants key off.
- `style-nova` selects the component style. Other upstream styles are Vega, Maia, Lyra, Mira, Luma, Sera, and Rhea. Changing style means changing both the imported `style-*.css` in the Make target and the class on `<html>`.
- `.cn-native-select` exists in the stylesheet but has no Go component. `ui.Select` uses it for the filter bar's native `<select>`s.
- shadcn's `Input` generates a random ID when none is given, which would differ between the server render and the wasm render of the same page. Always pass `ID`.

## Rendering compatibility

Static components are ordinary `templ.Component`s. They work with Howl cold SSR, partial navigation and static export, and they compile for `js/wasm` — every Guard page is a `.client.templ` route rendered by `client/public/views.wasm`, so this is proven rather than assumed.

Interactive components use a shared vanilla-JavaScript bundle and DOM data attributes. Upstream behavior uses delegated listeners and mutation observers designed for server-swapped DOM, conceptually matching Howl's outlet swaps. Guard has not enabled the shared bundle: the v2 direct-import workflow is experimental, and its development handler looks for a locally copied `components/` directory. Dialog, select, popover, tooltip and similar controls are therefore off the table until a component is CLI-copied into the repo. Anything Guard needs to be interactive is a native element driven by `guard.js`.

## Universal component API

Most components accept:

| Prop | Type | Meaning |
| --- | --- | --- |
| `ID` | `string` | Rendered element ID |
| `Class` | `string` | Additional classes merged with defaults |
| `Attributes` | `templ.Attributes` | Arbitrary HTML, data and ARIA attributes |

Components compose through templ children:

```templ
@button.Button(button.Props{
    Variant: button.VariantOutline,
    Attributes: templ.Attributes{"aria-label": "Refresh"},
}) {
    Refresh
}
```

## Components exercised in Guard

### Button

Import `github.com/axadrn/shadcn-templ/v2/components/button`.

Variants: `VariantDefault`, `VariantSecondary`, `VariantOutline`, `VariantGhost`, `VariantDestructive`, `VariantLink`.

Sizes: `SizeDefault`, `SizeXs`, `SizeSm`, `SizeLg`, `SizeIcon`, `SizeIconXs`, `SizeIconSm`, `SizeIconLg`.

Set `Href` to render an anchor. Types are `TypeButton`, `TypeSubmit`, and `TypeReset`; the default is `TypeButton`.

### Input and Textarea

Import `github.com/axadrn/shadcn-templ/v2/components/input` and `.../textarea`.

`input.Props`: `Name`, `Type`, `Value`, `Form`, `Placeholder`, `Disabled`, `ReadOnly`, `Required`, `Accept`. Textarea adds `Rows`. `min`/`max`/`autocomplete` and other native attributes go through `Attributes`. Always set `ID` (see Guard integration).

### Table

Import `github.com/axadrn/shadcn-templ/v2/components/table`.

`table.Table` renders its own `overflow-x-auto` container, then `table.Header`, `table.Body`, `table.Footer`, `table.Row`, `table.Head`, `table.Cell`, `table.Caption`. Guard's tables put `data-*-rows` on `table.Body` and let `guard.js` build the rows, which is why the row and cell classes are duplicated as literals in that file.

### Item

Import `github.com/axadrn/shadcn-templ/v2/components/item`.

`item.Group` wraps `item.Item` (`Variant`, `Size`, `Href`), each composing `item.Media`, `item.Content`, `item.Title`, `item.Description`, `item.Actions`. Guard uses it for the settings sub-navigation.

### Empty

Import `github.com/axadrn/shadcn-templ/v2/components/empty`.

`empty.Empty` composes `empty.Header`, `empty.Media`, `empty.Title`, `empty.Description`, `empty.Content`. `cn-empty` sets `border-dashed` but no border width — add `border` yourself.

### Field

Import `github.com/axadrn/shadcn-templ/v2/components/field`.

Compose `field.Field`, `field.Label`, `field.Description`, and `field.Error`. `LabelProps.For` should match the input ID. Orientations are vertical, horizontal, and responsive.

### Card

Import `github.com/axadrn/shadcn-templ/v2/components/card`.

`card.Card` contains `card.Header`, `card.Title`, `card.Description`, optional `card.Action`, `card.Content`, and `card.Footer`.

### Badge and Alert

Badge variants mirror the common shadcn variants. An alert contains `alert.Title`, `alert.Description`, and optional `alert.Action`; it supports default and destructive variants.

style-nova has no warning or success badge, and Guard needs both for severity and span status. `guard.js` builds those two from theme tokens (`bg-warning/15 text-warning`, `bg-primary/15 text-primary`) on top of the plain `cn-badge` base rather than forking the component.

## Interactive components

The copied-source workflow renders `@components.Scripts()` once in the application shell and mounts `components.ScriptsHandler()` at `GET /components/{bundle}`. Do not add per-page script tags. The bundle contains each installed component's IIFE and is intended to handle dynamic DOM swaps.

Guard does not run this bundle. Before adopting interactive controls, verify:

1. Cold-load initialization.
2. Navigation into the route through Howl partial navigation.
3. Navigation away and back without duplicate listeners or stale portals.
4. Escape, focus trap, focus return, outside click, and screen-reader labels.
5. CSP nonce behavior.
6. Production and development bundle serving.

## Customization

There are three levels:

1. Theme tokens in `client/styles/app.css`.
2. `Class` and `Attributes` at each use site. `Class` is merged with tailwind-merge, so `card.Props{Class: "py-0 gap-0"}` really does cancel the card's own padding.
3. CLI-copy a component into Guard when its structure or behavior must change. This is the supported shadcn ownership model.

Do not edit the Go module cache. Copy selected components into a leaf package such as `client/components` and commit them.

## Commands

```bash
make css   # rebuild client/public/app.css — the only target that needs Node
make       # routes + templ generation + wasm + Guard binary
go test ./...
```

`make css` writes the gitignored `client/styles/app.sources.css` with the resolved module-cache path, then compiles `client/styles/app.css`. The output is committed so normal builds and CI remain offline.

## Upstream provenance

This guide was derived from documentation and source in the pinned v2.0.0-beta.3 module:

- `internal/service/content/docs/introduction.md`
- `internal/service/content/docs/installation.md`
- `internal/service/content/docs/import-workflow.md`
- `internal/service/content/docs/components/*.md`
- `components/*/*.templ` and `components/*/*.js`

When upgrading, review those local module files and update this guide with the code.
