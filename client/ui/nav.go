package ui

import "github.com/mirairoad/howl-go/core/router"

// navOrder is the sidebar's running order, one slice per group, drawn with a
// divider between the groups: first the two pages you keep open, then the three
// signals you go digging through. Only the order lives here — the labels still
// come from the route table, so renaming a page renames it in the sidebar.
var navOrder = [][]string{
	{"/", "/views"},
	{"/logs", "/traces", "/metrics"},
	{"/cluster", "/registries", "/storage", "/secrets"},
}

// navHidden lists routes that reach the sidebar some other way. /settings is
// the gear in the footer card, so it must not also be a nav row.
// /login reaches nobody through the sidebar: it is the one page rendered
// outside the shell, for somebody who cannot see a sidebar yet.
var navHidden = map[string]bool{"/settings": true, "/login": true}

// NavGroups arranges nav routes into the sidebar's groups.
//
// A pattern named here but absent from the route table is skipped, and a route
// navOrder does not name — a page added after this list was written — joins the
// last group instead of vanishing. Dropping off the sidebar is the worse
// failure of the two: the page still exists, it is just unreachable, and
// nothing about the omission is visible.
func NavGroups(routes []router.Route) [][]router.Route {
	byPattern := make(map[string]router.Route, len(routes))
	for _, route := range routes {
		byPattern[route.Pattern] = route
	}
	placed := make(map[string]bool, len(routes))
	groups := make([][]router.Route, 0, len(navOrder))
	for _, patterns := range navOrder {
		var group []router.Route
		for _, pattern := range patterns {
			route, ok := byPattern[pattern]
			if !ok || navHidden[pattern] {
				continue
			}
			placed[pattern] = true
			group = append(group, route)
		}
		groups = append(groups, group)
	}
	var rest []router.Route
	for _, route := range routes {
		if placed[route.Pattern] || navHidden[route.Pattern] {
			continue
		}
		rest = append(rest, route)
	}
	if len(rest) > 0 && len(groups) > 0 {
		groups[len(groups)-1] = append(groups[len(groups)-1], rest...)
	}
	// An empty group would draw a divider with nothing on one side of it.
	out := groups[:0]
	for _, group := range groups {
		if len(group) > 0 {
			out = append(out, group)
		}
	}
	return out
}
