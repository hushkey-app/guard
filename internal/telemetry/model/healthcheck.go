package model

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// HealthCheck is an HTTP service guard watches from the server.
//
// It is deliberately independent of Node. A load balancer, a public web app
// and a managed service are useful checks without being machines Guard can SSH
// into; one machine may also run several services. NodeID is therefore only an
// optional provenance link used when an older machine probe is migrated.
type HealthCheck struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	URL             string    `json:"url"`
	Enabled         bool      `json:"enabled"`
	IntervalSeconds int       `json:"interval_seconds"`
	Public          bool      `json:"public"`
	PublicName      string    `json:"public_name,omitempty"`
	NodeID          int64     `json:"node_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`

	Status     string    `json:"status"`
	StatusCode int       `json:"status_code,omitempty"`
	LatencyMS  float64   `json:"latency_ms,omitempty"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checked_at,omitempty"`
	Uptime     float64   `json:"uptime"`
	Checks     int       `json:"checks"`
	History    []float64 `json:"history,omitempty"`
}

func (h HealthCheck) Interval() time.Duration {
	seconds := h.IntervalSeconds
	if seconds < MinIntervalSeconds {
		seconds = DefaultIntervalSeconds
	}
	return time.Duration(seconds) * time.Second
}

func (h HealthCheck) DisplayName() string {
	if name := strings.TrimSpace(h.PublicName); name != "" {
		return name
	}
	return strings.TrimSpace(h.Name)
}

func (h HealthCheck) Validate() error {
	if strings.TrimSpace(h.Name) == "" {
		return errors.New("name is required")
	}
	if len(h.Name) > 80 {
		return errors.New("name must be 80 characters or fewer")
	}
	if len(h.PublicName) > 80 {
		return errors.New("public name must be 80 characters or fewer")
	}
	if err := ValidateNodeURL(strings.TrimSpace(h.URL)); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if h.IntervalSeconds != 0 && (h.IntervalSeconds < MinIntervalSeconds || h.IntervalSeconds > MaxIntervalSeconds) {
		return fmt.Errorf("check interval must be between %d and %d seconds", MinIntervalSeconds, MaxIntervalSeconds)
	}
	if h.NodeID < 0 {
		return errors.New("machine id cannot be negative")
	}
	return nil
}
