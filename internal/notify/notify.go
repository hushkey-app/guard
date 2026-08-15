// Package notify is the one way anything in guard tells the outside world
// something happened.
//
// It exists as a package rather than as a method on the thing that noticed
// because three different watchers want it and there will be a fourth: a
// scheduled command that has stopped succeeding, a machine over a threshold,
// a saved view whose number crossed a line. None of those should own an HTTP
// client, a token format or a retry policy, and three copies of "POST some
// JSON with a bearer token" is three places to fix the day somebody's endpoint
// wants a different header.
//
// What it deliberately does not do is speak any messaging app's API. Between
// "the backup is late" and a phone sits a relay that already exists — a Slack
// hook, an n8n flow, a four-line handler — and what guard owes it is one
// authenticated POST with the facts in it, not a Slack SDK that has to be
// updated when Slack moves.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// An Event is something worth telling somebody about.
//
// Kind and Subject are the machine-readable pair: Kind says what sort of thing
// this is, Subject names the exact thing it happened to. A receiver routing
// alerts reads those two; a human reads Message; anything that wants to draw a
// graph or set a colour reads Fields, which is where the *parameters* live —
// the CPU percentage that tripped the rule, the uptime that fell, the value a
// view returned.
//
// State is what makes this usable as an alert rather than a stream of noise:
// the same rule fires "firing" once and "resolved" once, so a receiver can
// close its own incident instead of guessing from silence.
type Event struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Subject string    `json:"subject"`
	State   string    `json:"state"`
	Title   string    `json:"title"`
	Message string    `json:"message"`
	// Fields carries whatever the sender measured, named as the receiver would
	// want to filter on it: cpu_percent, uptime_percent, latency_ms, value.
	Fields map[string]any `json:"fields,omitempty"`
	// Text is the same sentence as Message under the key a chat webhook
	// renders. Duplication on purpose: one URL then works for a Slack hook and
	// for something that parses the payload properly, which is the difference
	// between this feature being useful in five minutes and in an afternoon.
	Text string `json:"text"`
}

const (
	StateFiring   = "firing"
	StateResolved = "resolved"
)

// Kinds. One string per thing that can raise an event, because a receiver
// routing by kind should not have to parse a sentence.
const (
	KindScheduleStale = "schedule.stale"
	KindClusterRule   = "cluster.rule"
	KindViewRule      = "view.rule"
	KindTest          = "test"
)

// A Destination is where an event goes: a URL, and the credential it wants.
//
// Named, because "so I can direct where I want" is the whole point — the
// backups go to the channel the person who owns the database watches, and the
// page-me rules go somewhere that wakes somebody up.
type Destination struct {
	ID    int64
	Name  string
	URL   string
	Token string
	// Header is where the token goes, defaulting to Authorization, where a
	// bare token is made a Bearer credential. An app wanting X-Api-Key names
	// it and gets the token verbatim.
	Header string
}

// Sender delivers one event to one destination. An interface so the watchers
// can be tested without a listening socket, and so a future delivery path —
// a queue, a second protocol — is a type rather than a rewrite.
type Sender interface {
	Send(ctx context.Context, to Destination, event Event) error
}

// Webhook is the delivery path guard actually ships: one POST, JSON body.
//
// Its own client on purpose. An alert that a machine's jobs are failing must
// not travel over the machinery that runs those jobs, and a client shared with
// the prober would have that alert queueing behind health checks.
type Webhook struct {
	Client  *http.Client
	Timeout time.Duration
}

const defaultTimeout = 10 * time.Second

func (w *Webhook) Send(ctx context.Context, to Destination, event Event) error {
	if strings.TrimSpace(to.URL) == "" {
		return fmt.Errorf("destination %q has no URL", to.Name)
	}
	if event.Text == "" {
		event.Text = event.Message
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, to.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if header, value := to.credential(); header != "" {
		request.Header.Set(header, value)
	}
	response, err := w.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		// A refusal is an error and not a delivery: the caller leaves its
		// "already told them" flag unset, so the next pass tries again rather
		// than an outage being swallowed by a 401.
		return fmt.Errorf("%s answered %s", to.Name, response.Status)
	}
	return nil
}

func (w *Webhook) client() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	w.Client = &http.Client{Timeout: timeout}
	return w.Client
}

// credential is the header and value this destination's token should be sent
// as, or two empty strings when it has none — a Slack or Discord incoming
// webhook wants no credential at all, because the URL *is* the secret.
func (d Destination) credential() (string, string) {
	token := strings.TrimSpace(d.Token)
	if token == "" {
		return "", ""
	}
	header := strings.TrimSpace(d.Header)
	if header == "" {
		header = "Authorization"
	}
	// A token that already names its own scheme is sent as written: "Bot xxx"
	// is what a bot API wants and "Bearer Bot xxx" is what nothing wants. A
	// custom header is never rewritten — an X-Api-Key with "Bearer " glued to
	// the front is a key that does not work.
	if strings.EqualFold(header, "Authorization") && !strings.Contains(token, " ") {
		token = "Bearer " + token
	}
	return header, token
}

// Discard is the sender an instance with nothing configured gets. Every
// watcher logs before it delivers, so a guard with no destinations is not a
// guard that misses things — it is one where the log is the only record.
type Discard struct{}

func (Discard) Send(context.Context, Destination, Event) error { return nil }
