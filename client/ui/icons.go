package ui

// The icon set, as data rather than as one templ per icon.
//
// Guard had two hand-written `templ IconX()` blocks, which is fine for two and
// becomes a wall at twenty: every icon repeats the same <svg> element, and
// adding one means writing markup rather than adding a row. Here an icon is its
// inner paths plus the few attributes that actually vary, and Icon renders the
// wrapper. Same shape as the registry in hushkey's menu-icon, so the mental
// model carries over.
//
// Everything is 24x24, stroke currentColor at 1.5, no fill — the outline weight
// the rest of the interface is drawn at. An icon that needs to be filled says
// so with Fill, and then leaves Stroke empty.
type IconDef struct {
	// ViewBox defaults to "0 0 24 24".
	ViewBox string
	// Fill defaults to "none". Set it to "currentColor" for a solid icon, and
	// leave Stroke empty when you do.
	Fill string
	// Stroke defaults to "currentColor" unless Fill is set.
	Stroke      string
	StrokeWidth string
	// Body is the inner SVG markup: paths, circles, nothing else.
	Body string
}

// Icons is the registry. Keys are the names Icon() and the sidebar ask for;
// an unknown name renders nothing rather than a broken glyph, because a missing
// icon should cost a missing icon and not a missing row.
var Icons = map[string]IconDef{
	"overview": {Body: `<path d="M3 12l9-8 9 8"/><path d="M5 10v10h14V10"/><path d="M9 20v-6h6v6"/>`},
	"checks":   {Body: `<path d="M3 12h4l2-7 4 14 2-7h6"/>`},
	"views":    {Body: `<rect x="3" y="4" width="18" height="7" rx="1.5"/><rect x="3" y="14" width="8" height="6" rx="1.5"/><rect x="15" y="14" width="6" height="6" rx="1.5"/>`},
	"logs":     {Body: `<path d="M5 4h14v16H5z"/><path d="M8 8h8"/><path d="M8 12h8"/><path d="M8 16h5"/>`},
	"traces":   {Body: `<circle cx="6" cy="6" r="2"/><circle cx="18" cy="12" r="2"/><circle cx="8" cy="18" r="2"/><path d="M8 6h6a2 2 0 0 1 2 2v2"/><path d="M16 14v0a2 2 0 0 1-2 2h-4"/>`},
	"metrics":  {Body: `<path d="M4 19V5"/><path d="M4 19h16"/><path d="M8 16v-5"/><path d="M13 16V8"/><path d="M18 16v-3"/>`},
	// Drawn on the same axes as metrics, because it is the same kind of
	// picture — but a line rising to an arrow rather than bars, since what this
	// page answers is which way the numbers went and not what they were.
	"analytics":  {Body: `<path d="M4 19V5"/><path d="M4 19h16"/><path d="m7 16 3.5-3.5 2.5 2.5L19 9"/><path d="M15 9h4v4"/>`},
	"cluster":    {Body: `<rect x="3" y="4" width="7" height="7" rx="1.5"/><rect x="14" y="4" width="7" height="7" rx="1.5"/><rect x="3" y="15" width="7" height="5" rx="1.5"/><rect x="14" y="15" width="7" height="5" rx="1.5"/>`},
	"deploys":    {Body: `<path d="M12 3l3 4h-2v6h-2V7H9z"/><path d="M4 14v5a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-5"/><path d="M8 14h8"/>`},
	"registries": {Body: `<path d="M3 8l9-4 9 4-9 4z"/><path d="M3 12l9 4 9-4"/><path d="M3 16l9 4 9-4"/>`},
	"storage":    {Body: `<ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v6c0 1.66 3.58 3 8 3s8-1.34 8-3V6"/><path d="M4 12v6c0 1.66 3.58 3 8 3s8-1.34 8-3v-6"/>`},
	"secrets":    {Body: `<rect x="5" y="10" width="14" height="10" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/><path d="M12 14v2"/>`},
	"settings":   {Body: `<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.86 2.86-.06-.06A1.7 1.7 0 0 0 15 19.4a1.7 1.7 0 0 0-1 .6 1.7 1.7 0 0 0-.4 1.1V21H9.55v-.09A1.7 1.7 0 0 0 8.5 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.86-2.86.06-.06A1.7 1.7 0 0 0 4.1 15a1.7 1.7 0 0 0-.6-1 1.7 1.7 0 0 0-1.1-.4H2.3V9.55h.09A1.7 1.7 0 0 0 4.1 8.5a1.7 1.7 0 0 0-.34-1.88l-.06-.06L6.56 3.7l.06.06A1.7 1.7 0 0 0 8.5 4.1a1.7 1.7 0 0 0 1-.6 1.7 1.7 0 0 0 .4-1.1V2.3h4.05v.09A1.7 1.7 0 0 0 15 4.1a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.86 2.86-.06.06A1.7 1.7 0 0 0 19.4 8.5a1.7 1.7 0 0 0 .6 1 1.7 1.7 0 0 0 1.1.4h.09v4.05h-.09a1.7 1.7 0 0 0-1.1.4Z"/>`},
	"sign-out":   {Body: `<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><path d="m16 17 5-5-5-5"/><path d="M21 12H9"/>`},
	"menu":       {Body: `<path d="M4 7h16"/><path d="M4 12h16"/><path d="M4 17h16"/>`},
	"close":      {Body: `<path d="M6 6l12 12"/><path d="M18 6 6 18"/>`},
	"plus":       {Body: `<path d="M12 5v14"/><path d="M5 12h14"/>`},
	"server":     {Body: `<rect x="3" y="4" width="18" height="6" rx="2"/><rect x="3" y="14" width="18" height="6" rx="2"/><path d="M7 7h.01"/><path d="M7 17h.01"/><path d="M11 7h6"/><path d="M11 17h6"/>`},
	"alert":      {Body: `<path d="M12 3 2.8 19h18.4Z"/><path d="M12 9v4"/><path d="M12 16h.01"/>`},
	"activity":   {Body: `<path d="M3 12h4l2.5-6 5 12 2.5-6h4"/>`},
	"pencil":     {Body: `<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/>`},
	"trash":      {Body: `<path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="M19 6l-1 14H6L5 6"/><path d="M10 11v5"/><path d="M14 11v5"/>`},
	"back":       {Body: `<path d="M15 6l-6 6 6 6"/>`},
	"dot":        {Fill: "currentColor", Body: `<circle cx="12" cy="12" r="4"/>`},
	"info":       {Body: `<circle cx="12" cy="12" r="9"/><path d="M12 11v5"/><path d="M12 8h.01"/>`},
	"chevron":    {Body: `<path d="m6 9 6 6 6-6"/>`},
}

// icon resolves a name and fills in the defaults, so a registry entry only
// states what differs.
func icon(name string) (IconDef, bool) {
	def, ok := Icons[name]
	if !ok {
		return IconDef{}, false
	}
	if def.ViewBox == "" {
		def.ViewBox = "0 0 24 24"
	}
	if def.Fill == "" {
		def.Fill = "none"
	}
	if def.Stroke == "" && def.Fill == "none" {
		def.Stroke = "currentColor"
	}
	if def.StrokeWidth == "" && def.Stroke != "" {
		def.StrokeWidth = "1.5"
	}
	return def, true
}
