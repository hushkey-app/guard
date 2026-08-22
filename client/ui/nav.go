package ui

import "github.com/mirairoad/howl-go/core/router"

// NavGroup is one titled run of sidebar rows.
type NavGroup struct {
	// Label heads the group. It names a kind of work rather than a feature,
	// which is what makes eight rows scannable: you look for "the signals",
	// not for "traces".
	Label  string
	Routes []router.Route
}

// navOrder is the sidebar's running order and its group headings: first the
// pages you keep open, then the three OTLP signals you go digging through, then
// the things the cluster is made of. Only the order and the headings live here
// — row labels still come from the route table, so renaming a page renames it
// in the sidebar.
var navOrder = []struct {
	Label    string
	Patterns []string
}{
	{"Watch", []string{"/", "/views"}},
	{"Signals", []string{"/logs", "/traces", "/metrics"}},
	{"Infrastructure", []string{"/cluster", "/deploys", "/registries", "/storage", "/secrets"}},
}

// navIcons maps a nav route to its glyph in the icon registry. Kept beside
// navOrder because both are the same kind of fact — how this route presents in
// the sidebar — and neither belongs in the route table, which describes what
// the application serves rather than how it is drawn.
//
// A route with no entry falls back to a dot, so a page added tomorrow gets a
// row that lines up with the others instead of a ragged one.
var navIcons = map[string]string{
	"/":           "overview",
	"/views":      "views",
	"/logs":       "logs",
	"/traces":     "traces",
	"/metrics":    "metrics",
	"/cluster":    "cluster",
	"/deploys":    "deploys",
	"/registries": "registries",
	"/storage":    "storage",
	"/secrets":    "secrets",
	"/settings":   "settings",
}

// NavIcon is the glyph for a nav route.
func NavIcon(pattern string) string {
	if name, ok := navIcons[pattern]; ok {
		return name
	}
	return "dot"
}

// navHidden lists routes that reach the sidebar some other way. /settings is
// the gear in the footer card, so it must not also be a nav row.
//
// /login and /status are not listed and do not need to be: they are `.raw`
// routes, and a raw route is its own document — no shell, no sidebar. Listing
// one in the chrome that it does not render is a contradiction, so NavGroups
// drops every raw route instead of naming them here. A raw page added tomorrow
// is then handled without an edit.
var navHidden = map[string]bool{"/settings": true}

// hidden reports whether a route should stay out of the sidebar.
func hidden(route router.Route) bool { return route.Raw || navHidden[route.Pattern] }

// NavGroups arranges nav routes into the sidebar's groups.
//
// A pattern named here but absent from the route table is skipped, and a route
// navOrder does not name — a page added after this list was written — joins the
// last group instead of vanishing. Dropping off the sidebar is the worse
// failure of the two: the page still exists, it is just unreachable, and
// nothing about the omission is visible.
func NavGroups(routes []router.Route) []NavGroup {
	byPattern := make(map[string]router.Route, len(routes))
	for _, route := range routes {
		byPattern[route.Pattern] = route
	}
	placed := make(map[string]bool, len(routes))
	groups := make([]NavGroup, 0, len(navOrder))
	for _, spec := range navOrder {
		group := NavGroup{Label: spec.Label}
		for _, pattern := range spec.Patterns {
			route, ok := byPattern[pattern]
			if !ok || hidden(route) {
				continue
			}
			placed[pattern] = true
			group.Routes = append(group.Routes, route)
		}
		groups = append(groups, group)
	}
	var rest []router.Route
	for _, route := range routes {
		if placed[route.Pattern] || hidden(route) {
			continue
		}
		rest = append(rest, route)
	}
	if len(rest) > 0 && len(groups) > 0 {
		groups[len(groups)-1].Routes = append(groups[len(groups)-1].Routes, rest...)
	}
	// An empty group would draw a heading over nothing.
	out := groups[:0]
	for _, group := range groups {
		if len(group.Routes) > 0 {
			out = append(out, group)
		}
	}
	return out
}
