# client/ui — shared page furniture

**Scoped gate:** `make css && make test`

- **Icons are one registry**, `icons.go`, not one templ per icon. A row is the
  inner paths plus only the attributes that differ from the default (24×24,
  `stroke="currentColor"`, width 1.5, no fill). Use `@ui.Icon("name", "size-4")`
  and size with the class — never a `width` attribute. An unknown name renders
  nothing, on purpose.
- **`nav.go` owns order and grouping; the route table owns the labels**, so
  renaming a page renames its sidebar row. A new page is a pattern in
  `navOrder` and an entry in `navIcons`. A route in neither still appears — it
  joins the last group, because dropping off the sidebar is the worse failure.
- **`<html class="dark style-nova">` is load-bearing.** `style-nova.css` nests
  every `cn-*` rule under `.style-nova`; drop the class and every component
  silently unstyles.
- Interactive shadcn components (select, dialog, popover) need an upstream JS
  bundle this workflow does not serve. Guard uses **static components only** —
  filter controls are native elements wearing `cn-native-select`.

Read `docs/shadcn-templ.md` — the offline digest of the exact beta pinned here.
Do not guess React shadcn APIs.
