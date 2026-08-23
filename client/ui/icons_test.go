package ui

import (
	"strings"
	"testing"
)

// A name the registry does not know renders nothing, and a nav route with no
// entry falls back to a dot. Both are the right answer in a browser and the
// wrong one in a build: the row that lost its glyph looks like somebody's
// decision. So every name the sidebar asks for is pinned here.
func TestNamedGlyphsAreRegistered(t *testing.T) {
	for _, name := range navIcons {
		if _, ok := icon(name); !ok {
			t.Errorf("%q is asked for by name and is not in the registry", name)
		}
	}
}

// The defaults exist so a row states only what differs; a row that states
// nothing must still come out as the outline weight the rest of the interface
// is drawn at, and a solid one must not also be stroked.
func TestIconsResolveToOneWeight(t *testing.T) {
	for name := range Icons {
		def, _ := icon(name)
		if strings.TrimSpace(def.Body) == "" {
			t.Errorf("%q has no body", name)
			continue
		}
		if def.ViewBox == "" {
			t.Errorf("%q has no view box", name)
		}
		if def.Fill == "currentColor" {
			if def.Stroke != "" {
				t.Errorf("%q is filled and stroked; a solid icon leaves Stroke empty", name)
			}
			continue
		}
		if def.Stroke != "currentColor" || def.StrokeWidth != "1.5" {
			t.Errorf("%q is drawn at %s/%s, not currentColor/1.5", name, def.Stroke, def.StrokeWidth)
		}
	}
}
