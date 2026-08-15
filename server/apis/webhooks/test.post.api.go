package webhooks

import (
	"database/sql"
	"errors"
	"time"

	"github.com/hushkey-app/guard/internal/notify"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// TestRequest names the destination to try.
type TestRequest struct {
	ID int64 `json:"id"`
}

// TestResult is what the far end said.
type TestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Test sends one event and reports what came back.
//
// It exists because every other way of finding out whether a destination works
// is an incident. A token typed with a trailing space, a URL that 404s, a
// header the app does not read — all three look exactly like a quiet week
// until the night something is actually wrong.
//
// The event it sends is a real one, of kind "test", so whatever is on the far
// end sees the shape it will get at 4am rather than a special case.
var Test = api.Define(api.Spec[api.None, TestRequest, TestResult]{
	Name:  "Test Event Destination",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, TestRequest]) (TestResult, error) {
		destination, err := store.Get().DestinationFor(r.Body.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return TestResult{}, api.NotFound("no such destination")
		}
		if err != nil {
			return TestResult{}, err
		}
		now := time.Now()
		sender := &notify.Webhook{}
		sendErr := sender.Send(r.Context(), destination, notify.Event{
			At:      now.UTC(),
			Kind:    notify.KindTest,
			Subject: destination.Name,
			State:   notify.StateFiring,
			Title:   "Test event from guard",
			Message: "This is guard testing the destination " + destination.Name + ". Nothing is wrong.",
			Fields:  map[string]any{"test": true},
		})
		failure := ""
		if sendErr != nil {
			failure = sendErr.Error()
		}
		// Recorded like any other delivery: a test that failed is exactly the
		// state the page should keep showing until somebody fixes it.
		if err := store.Get().RecordDelivery(r.Body.ID, now, failure); err != nil {
			return TestResult{}, err
		}
		// A refusal is a 200 carrying the reason. "The webhook answered 401" is
		// the answer to the question that was asked.
		return TestResult{OK: sendErr == nil, Error: failure}, nil
	},
})
