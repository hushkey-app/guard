package ingest

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func beaconJSON(t *testing.T, b model.Beacon) string {
	t.Helper()
	encoded, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func aBeacon() model.Beacon {
	return model.Beacon{
		Session: "6f2a9c1d4e8b7a30",
		Path:    "/pricing",
		Events: []model.TrackEvent{
			{Name: "page_view"},
			{Name: "signup_click", Props: map[string]string{"plan": "team"}},
		},
	}
}

// The analytics door is the browser door, so it is off until an origin is
// named — no switch of its own, because a second switch would be a second
// answer to whether guard accepts anything from a stranger.
func TestBeaconIntakeIsOffUntilOriginsAreNamed(t *testing.T) {
	_, server := browserServer(t, Browser{})
	response := post(t, server.URL+"/v1/rum/events", "https://app.example.com", beaconJSON(t, aBeacon()))
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — nothing should be mounted", response.StatusCode)
	}
}

// The happy path, all the way to the rollup: the door is only worth anything
// if what came through it can be read back off the grid.
func TestBeaconLandsInTheRollup(t *testing.T) {
	store, server := browserServer(t, Browser{Origins: []string{"https://app.example.com"}})

	response := post(t, server.URL+"/v1/rum/events", "https://app.example.com", beaconJSON(t, aBeacon()))
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.StatusCode)
	}

	now := time.Now().UTC()
	rows, err := store.AnalyticsPaths(now.Add(-24*time.Hour), now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Path != "/pricing" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Views != 1 || rows[0].Sessions != 1 {
		t.Errorf("views = %d, sessions = %d, want 1 and 1", rows[0].Views, rows[0].Sessions)
	}
	cell, ok := rows[0].Actions["signup_click"]
	if !ok {
		t.Fatalf("actions = %+v", rows[0].Actions)
	}
	if cell.Events != 1 || cell.Sessions != 1 || cell.Rate != 1 {
		t.Errorf("cell = %+v", cell)
	}
}

func TestBeaconOriginRules(t *testing.T) {
	_, server := browserServer(t, Browser{Origins: []string{"https://app.example.com"}})
	body := beaconJSON(t, aBeacon())

	for _, tc := range []struct {
		name, origin string
		want         int
	}{
		{"the allowed origin", "https://app.example.com", http.StatusNoContent},
		{"a different origin", "https://evil.example.com", http.StatusForbidden},
		// The relay deployment, which is the recommended one because an ad
		// blocker will not let the direct post out of the page at all.
		{"no origin at all", "", http.StatusNoContent},
	} {
		if got := post(t, server.URL+"/v1/rum/events", tc.origin, body).StatusCode; got != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// The same 256 KB budget as the rest of the door, enforced before decoding
// rather than after: the point is not to have read it.
func TestBeaconBodyLimit(t *testing.T) {
	_, server := browserServer(t, Browser{Origins: []string{"*"}})
	huge := aBeacon()
	for range maxBrowserBody/model.MaxPropValue + 8 {
		huge.Events = append(huge.Events, model.TrackEvent{
			Name:  "filler",
			Props: map[string]string{"x": strings.Repeat("x", model.MaxPropValue)},
		})
	}
	body := beaconJSON(t, huge)
	if len(body) <= maxBrowserBody {
		t.Fatalf("the test body is only %d bytes", len(body))
	}
	if got := post(t, server.URL+"/v1/rum/events", "https://app.example.com", body).StatusCode; got != http.StatusBadRequest {
		t.Fatalf("a %d byte body answered %d", len(body), got)
	}
}

func TestBeaconRateLimit(t *testing.T) {
	_, server := browserServer(t, Browser{Origins: []string{"*"}, PerMinute: 3})
	body := beaconJSON(t, aBeacon())

	var limited bool
	for range 5 {
		if post(t, server.URL+"/v1/rum/events", "https://anything.example.com", body).StatusCode == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Fatal("five beacons against a budget of three were all accepted")
	}
}

// Every edge limit, answered at the door. A refused beacon is refused whole:
// a batch stored halfway is a count nobody can reason about, and the tracker
// that sent it has a bug worth hearing about in full.
func TestBeaconEdgeLimits(t *testing.T) {
	tooMany := aBeacon()
	for range model.MaxBeaconEvents {
		tooMany.Events = append(tooMany.Events, model.TrackEvent{Name: "click"})
	}
	tooManyProps := aBeacon()
	tooManyProps.Events[1].Props = map[string]string{}
	for i := range model.MaxEventProps + 1 {
		tooManyProps.Events[1].Props[string(rune('a'+i))] = "x"
	}
	longName := aBeacon()
	longName.Events[1].Name = strings.Repeat("n", model.MaxActionName+1)
	shoutedName := aBeacon()
	shoutedName.Events[1].Name = "Signup_Click"
	longValue := aBeacon()
	longValue.Events[1].Props["plan"] = strings.Repeat("x", model.MaxPropValue+1)
	longPath := aBeacon()
	longPath.Path = "/" + strings.Repeat("p", model.MaxAnalyticsPath)
	noSession := aBeacon()
	noSession.Session = ""
	noEvents := aBeacon()
	noEvents.Events = nil

	store, server := browserServer(t, Browser{Origins: []string{"*"}, PerMinute: 1000})
	for _, tc := range []struct {
		name   string
		beacon model.Beacon
	}{
		{"over the batch ceiling", tooMany},
		{"an action name too long", longName},
		{"an action name outside the alphabet", shoutedName},
		{"too many props on one event", tooManyProps},
		{"a prop value too long", longValue},
		{"a path too long", longPath},
		{"no session id", noSession},
		{"no events", noEvents},
	} {
		response := post(t, server.URL+"/v1/rum/events", "https://app.example.com", beaconJSON(t, tc.beacon))
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.name, response.StatusCode)
		}
	}

	// Refused whole: none of the eight left anything behind, including the
	// page_view every one of them carried.
	now := time.Now().UTC()
	rows, err := store.AnalyticsPaths(now.Add(-24*time.Hour), now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused beacon was stored: %+v", rows)
	}
}
