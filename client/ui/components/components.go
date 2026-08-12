// Package components is the dashboard's panel furniture: the card a panel
// lives in, the bodies each shape renders into, the view builder, and the
// toolbar above them.
//
// Almost everything here renders into a <template>. That is unusual enough to
// explain.
//
// A saved view is data the browser fetches — the page cannot know at render
// time how many panels there are or what they are called, and the views page is
// a .client route, so its Go code also runs in wasm where reaching SQLite is
// impossible. The markup therefore has to be produced in the browser. The
// choice is between building it with document.createElement in JavaScript, or
// declaring it once in templ and cloning it per panel.
//
// Cloning wins twice. The panel chrome stays real shadcn-templ markup —
// card.Card, button.Button, the same components every server-rendered page
// uses — instead of a JavaScript approximation of it that drifts the first time
// the theme changes. And every Tailwind class stays inside a .templ file, which
// is where the @source globs already look; a class that only ever appeared in a
// string literal in guard.js is a class Tailwind may or may not have emitted.
//
// So: the page renders the templates, client/public/views.js clones one per
// panel and fills the slots. The slots are data attributes, and they are the
// contract between this package and that file.
package components

import "strconv"

// Width is a panel's span of the twelve-column grid, clamped to something that
// can actually be laid out. It is emitted as an inline style rather than a
// Tailwind class because the value comes from the database: `col-span-7` in a
// class string the compiler never sees is a class Tailwind never emits.
func Width(width int) string {
	if width < 1 || width > 12 {
		width = 6
	}
	return "grid-column: span " + strconv.Itoa(width) + " / span " + strconv.Itoa(width) + ";"
}
