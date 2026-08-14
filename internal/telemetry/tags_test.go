package telemetry

import (
	"testing"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func TestNodeTagsRoundTrip(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	node, err := store.SaveNode(Node{
		Name: "db-1", URL: "https://db-1.example.com", Enabled: true,
		Tags: []model.NodeTag{
			{Label: "  postgres  ", Colour: "blue"},
			// No colour named: the default rather than a refusal.
			{Label: "prod"},
			// Dropped: an empty label, and the same label twice.
			{Label: "   "},
			{Label: "POSTGRES", Colour: "red"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(node.Tags) != 2 {
		t.Fatalf("stored %+v", node.Tags)
	}
	if node.Tags[0].Label != "postgres" || node.Tags[0].Colour != "blue" {
		t.Errorf("first tag is %+v", node.Tags[0])
	}
	if node.Tags[1].Colour != model.TagColours[0] {
		t.Errorf("a tag with no colour got %q, want %q", node.Tags[1].Colour, model.TagColours[0])
	}

	// Read back through the list, which is the path the dashboard walks.
	listed, err := store.Nodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || len(listed[0].Tags) != 2 || listed[0].Tags[0].Label != "postgres" {
		t.Fatalf("listed %+v", listed)
	}

	// An edit that says nothing about tags is an edit that keeps them: the
	// client sends them back with every save, and this is the store holding
	// up its side.
	node.Name = "db-1 (eu)"
	saved, err := store.SaveNode(node)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Tags) != 2 {
		t.Fatalf("a rename lost the tags: %+v", saved.Tags)
	}

	// And clearing them is possible, because an empty list is a decision.
	saved.Tags = nil
	cleared, err := store.SaveNode(saved)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Tags) != 0 {
		t.Fatalf("tags survived being cleared: %+v", cleared.Tags)
	}
}

func TestNodeTagsAreValidated(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	if _, err := store.SaveNode(Node{Name: "x", URL: "https://x.example.com",
		Tags: []model.NodeTag{{Label: "db", Colour: "chartreuse"}}}); err == nil {
		t.Fatal("a colour outside the palette was accepted")
	}

	many := make([]model.NodeTag, 0, model.MaxTagsPerNode+1)
	for i := 0; i <= model.MaxTagsPerNode; i++ {
		many = append(many, model.NodeTag{Label: string(rune('a' + i)), Colour: "blue"})
	}
	if _, err := store.SaveNode(Node{Name: "y", URL: "https://y.example.com", Tags: many}); err == nil {
		t.Fatalf("more than %d tags were accepted", model.MaxTagsPerNode)
	}
}

func TestDuplicateCarriesTags(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	node, err := store.SaveNode(Node{Name: "web-1", URL: "https://web-1.example.com", Enabled: true,
		Tags: []model.NodeTag{{Label: "nginx", Colour: "green"}}})
	if err != nil {
		t.Fatal(err)
	}
	copied, err := store.DuplicateNode(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(copied.Tags) != 1 || copied.Tags[0].Label != "nginx" || copied.Tags[0].Colour != "green" {
		t.Fatalf("the copy carries %+v", copied.Tags)
	}
}
