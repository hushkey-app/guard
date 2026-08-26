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

type HealthIncident struct {
	ID               int64                 `json:"id"`
	CheckID          int64                 `json:"check_id"`
	StartedAt        time.Time             `json:"started_at"`
	EndedAt          time.Time             `json:"ended_at"`
	DurationSeconds  int64                 `json:"duration_seconds"`
	Comment          string                `json:"comment,omitempty"`
	Severity         string                `json:"severity"`
	AllocatedMinutes int                   `json:"allocated_minutes"`
	Day              string                `json:"day"`
	Confirmed        bool                  `json:"confirmed"`
	Events           []HealthIncidentEvent `json:"events"`
}

type HealthIncidentEvent struct {
	CheckedAt  time.Time `json:"checked_at"`
	StatusCode int       `json:"status_code,omitempty"`
	Error      string    `json:"error"`
}

type HealthIncidentReport struct {
	Comment          string `json:"comment"`
	Severity         string `json:"severity"`
	AllocatedMinutes int    `json:"allocated_minutes"`
	Confirmed        bool   `json:"confirmed"`
}

type HealthIncidentBoard struct {
	Incidents        []HealthIncident `json:"incidents"`
	AvailableMinutes map[string]int   `json:"available_minutes"`
}

type HealthIncidentCreate struct {
	CheckID int64  `json:"check_id"`
	Day     string `json:"day"`
}

func (c HealthIncidentCreate) Validate() error {
	if c.CheckID <= 0 {
		return errors.New("check id is required")
	}
	if _, err := time.Parse("2006-01-02", c.Day); err != nil {
		return errors.New("day must be YYYY-MM-DD")
	}
	return nil
}

func (r HealthIncidentReport) Validate() error {
	r.Comment = strings.TrimSpace(r.Comment)
	if len(r.Comment) > 500 {
		return errors.New("incident comment must be 500 characters or fewer")
	}
	if r.Severity != "partial" && r.Severity != "major" {
		return errors.New("incident type must be partial or major outage")
	}
	if r.AllocatedMinutes < 0 {
		return errors.New("incident minutes cannot be negative")
	}
	if r.Confirmed && r.AllocatedMinutes == 0 {
		return errors.New("outage minutes are required before publishing")
	}
	return nil
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
