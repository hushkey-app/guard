package model

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// A Node is one machine guard watches from the outside.
//
// This is deliberately not the same idea as an Instance. An instance is derived
// from telemetry: it exists because something posted to guard, and it disappears
// when that stops — which is exactly when you most want to know about it. A node
// is declared, so guard can say "VPS-1 has been down for six minutes" about a
// machine that is not talking to anyone.
//
// One name, one URL. Anything richer — auth headers, expected bodies, TCP
// checks — is a different feature, and every one of them is a reason for a
// check to fail in a way nobody can read off the dashboard.
type Node struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	// Enabled stops the polling without losing the node. A machine taken down
	// for maintenance should not have to be deleted and retyped.
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// The rest is the latest check, carried alongside so the dashboard reads
	// the whole cluster in one request.
	Status     string    `json:"status"` // up | down | unknown
	StatusCode int       `json:"status_code,omitempty"`
	LatencyMS  float64   `json:"latency_ms,omitempty"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checked_at,omitempty"`
	// Uptime is the share of successful checks over the last day, and Checks is
	// how many there were. A 100% that is one check out of one is worth telling
	// apart from a 100% that is two thousand.
	Uptime float64 `json:"uptime"`
	Checks int     `json:"checks"`
	// History is the recent checks, oldest first, for the sparkline: 1 up,
	// 0 down.
	History []float64 `json:"history,omitempty"`
}

// Check is one probe of one node.
type Check struct {
	OK         bool      `json:"ok"`
	StatusCode int       `json:"status_code"`
	LatencyMS  float64   `json:"latency_ms"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
}

const (
	StatusUp      = "up"
	StatusDown    = "down"
	StatusUnknown = "unknown"
)

// ClusterSummary is the one-line answer: how many are up, and are any of them
// down right now.
type ClusterSummary struct {
	Nodes   int    `json:"nodes"`
	Up      int    `json:"up"`
	Down    int    `json:"down"`
	Unknown int    `json:"unknown"`
	Worst   string `json:"worst,omitempty"`
}

// Validate runs before the handler, and in the browser too — the settings form
// imports this package, so a URL guard could never poll is rejected before it
// costs a round trip.
func (n Node) Validate() error {
	if strings.TrimSpace(n.Name) == "" {
		return errors.New("name is required")
	}
	if len(n.Name) > 80 {
		return errors.New("name must be 80 characters or fewer")
	}
	return ValidateNodeURL(n.URL)
}

// ValidateNodeURL is the whole safety story for the prober.
//
// Guard makes an outbound request to whatever this says, on a timer, from
// inside whatever network it runs in. That is the entire point of the feature
// and also its only real risk, so the rule is narrow and explicit: an absolute
// http or https URL with a host. No file://, no gopher://, no scheme-relative
// path that resolves against guard's own origin.
func ValidateNodeURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("url is required")
	}
	if len(raw) > 2048 {
		return errors.New("url is too long")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url is not valid: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("url must start with http:// or https://")
	}
	if parsed.Host == "" {
		return errors.New("url needs a host, like https://vps-1.example.com/api/health")
	}
	return nil
}
