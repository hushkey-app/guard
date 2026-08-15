package model

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A Schedule is when a stored command runs by itself.
//
// It is the same five fields cron has used since 1975, plus an "@every 6h"
// form, and it is parsed here rather than taken from a library because the
// whole feature is one column on cluster_actions: an action with a schedule is
// run by guard on a timer, and an action without one is exactly what it was
// before — a button somebody presses.
//
// Times are UTC. A server's timezone is invisible from the dashboard, so
// "0 3 * * *" meaning 3am somewhere nobody can see would be a fact discovered
// by a backup landing at the wrong hour. The card says UTC next to it.
type Schedule struct {
	spec string
	// every is set by the "@every 6h" form, which is a period rather than a
	// calendar: it fires that long after the last run, so a dump every six
	// hours stays six hours apart instead of drifting into a fixed clock.
	every time.Duration
	// The calendar form as bitmasks — one bit per legal value in each field.
	minute, hour, dom, month, dow uint64
}

const (
	// MinScheduleInterval is what "@every" will accept. Below half a minute
	// this stops being a schedule and becomes a loop against somebody's
	// machine, over a fresh SSH handshake each time.
	MinScheduleInterval = 30 * time.Second
	// MaxScheduleInterval is a month. Past that a cron expression says it
	// better, and a period nobody can see the next fire of is a period nobody
	// notices has stopped.
	MaxScheduleInterval = 31 * 24 * time.Hour
)

// shorthands are the names people actually type. Kept because "@daily" is
// harder to get wrong than "0 0 * * *", and the wrong one runs somebody's
// backup sixty times an hour.
var shorthands = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

// ParseSchedule reads a schedule expression. The empty string is not an error:
// it is an action with no schedule, which is most of them.
func ParseSchedule(spec string) (Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Schedule{}, nil
	}
	lower := strings.ToLower(spec)
	if expanded, ok := shorthands[lower]; ok {
		lower = expanded
	}
	if strings.HasPrefix(lower, "@every") {
		rest := strings.TrimSpace(strings.TrimPrefix(lower, "@every"))
		every, err := time.ParseDuration(rest)
		if err != nil {
			return Schedule{}, fmt.Errorf("%q is not a duration: try @every 6h", rest)
		}
		if every < MinScheduleInterval {
			return Schedule{}, fmt.Errorf("a schedule may not run more often than every %s", MinScheduleInterval)
		}
		if every > MaxScheduleInterval {
			return Schedule{}, errors.New("a schedule that far apart is better written as a cron expression")
		}
		return Schedule{spec: spec, every: every}, nil
	}
	fields := strings.Fields(lower)
	if len(fields) != 5 {
		return Schedule{}, fmt.Errorf("a schedule is five fields (minute hour day month weekday) or @every 6h, not %d", len(fields))
	}
	var s Schedule
	s.spec = spec
	var err error
	if s.minute, err = parseField(fields[0], 0, 59, nil); err != nil {
		return Schedule{}, fmt.Errorf("minute: %w", err)
	}
	if s.hour, err = parseField(fields[1], 0, 23, nil); err != nil {
		return Schedule{}, fmt.Errorf("hour: %w", err)
	}
	if s.dom, err = parseField(fields[2], 1, 31, nil); err != nil {
		return Schedule{}, fmt.Errorf("day of month: %w", err)
	}
	if s.month, err = parseField(fields[3], 1, 12, months); err != nil {
		return Schedule{}, fmt.Errorf("month: %w", err)
	}
	// Sunday is both 0 and 7, because half the world's crontabs say one and
	// half say the other.
	if s.dow, err = parseField(strings.ReplaceAll(fields[4], "7", "0"), 0, 6, weekdays); err != nil {
		return Schedule{}, fmt.Errorf("weekday: %w", err)
	}
	return s, nil
}

var months = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var weekdays = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// parseField turns one cron field into a bitmask: "*", "5", "1-4", "*/15",
// "1-20/5" and any comma-separated mix of them.
func parseField(field string, low, high int, names map[string]int) (uint64, error) {
	var mask uint64
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, errors.New("empty value")
		}
		step := 1
		if slash := strings.Index(part, "/"); slash >= 0 {
			var err error
			step, err = strconv.Atoi(part[slash+1:])
			if err != nil || step <= 0 {
				return 0, fmt.Errorf("%q is not a step", part[slash+1:])
			}
			part = part[:slash]
		}
		start, end := low, high
		if part != "*" {
			bounds := strings.SplitN(part, "-", 2)
			var err error
			if start, err = parseValue(bounds[0], names); err != nil {
				return 0, err
			}
			end = start
			if len(bounds) == 2 {
				if end, err = parseValue(bounds[1], names); err != nil {
					return 0, err
				}
			} else if step > 1 {
				// "5/15" is the same as "5-59/15": a step from a single value
				// runs to the end of the field, which is what everybody who
				// writes it means.
				end = high
			}
		}
		if start < low || end > high || start > end {
			return 0, fmt.Errorf("%d-%d is outside %d-%d", start, end, low, high)
		}
		for value := start; value <= end; value += step {
			mask |= 1 << uint(value)
		}
	}
	return mask, nil
}

func parseValue(raw string, names map[string]int) (int, error) {
	raw = strings.TrimSpace(raw)
	if names != nil {
		if value, ok := names[raw]; ok {
			return value, nil
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", raw)
	}
	return value, nil
}

// Set reports whether there is a schedule at all.
func (s Schedule) Set() bool { return s.every > 0 || s.minute != 0 }

// Every is the period of an "@every" schedule, or zero for a calendar one.
func (s Schedule) Every() time.Duration { return s.every }

func (s Schedule) String() string { return s.spec }

// maxScheduleSearch bounds the calendar walk. Five years is long enough for
// "0 0 29 2 *" — the 29th of February — and short enough that an expression
// matching nothing at all answers rather than spins.
const maxScheduleSearch = 5 * 366 * 24 * time.Hour

// Next is the first fire strictly after `after`, or the zero time when there
// is no schedule (or none within five years, which is the same thing to the
// caller).
//
// For "@every" it is simply after+period: the anchor the scheduler passes is
// the last run, so a job that took twenty minutes waits its full period from
// when it finished rather than being immediately due again.
func (s Schedule) Next(after time.Time) time.Time {
	if s.every > 0 {
		return after.Add(s.every).UTC()
	}
	if !s.Set() {
		return time.Time{}
	}
	limit := after.Add(maxScheduleSearch)
	// Cron has minute resolution, so start at the top of the next minute.
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	for t.Before(limit) {
		if !s.matchesDay(t) {
			// Skip the whole day rather than its 1440 minutes: an expression
			// that fires once a year is otherwise half a million comparisons.
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
			continue
		}
		if s.bit(s.hour, t.Hour()) && s.bit(s.minute, t.Minute()) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

func (s Schedule) matchesDay(t time.Time) bool {
	if !s.bit(s.month, int(t.Month())) {
		return false
	}
	domRestricted := s.dom != fullMask(1, 31)
	dowRestricted := s.dow != fullMask(0, 6)
	dom := s.bit(s.dom, t.Day())
	dow := s.bit(s.dow, int(t.Weekday()))
	// The classic rule: when both the day of month and the weekday are
	// restricted, either one matching is enough — "0 0 1 * mon" is the first of
	// the month *and* every Monday, not the first when it falls on a Monday.
	if domRestricted && dowRestricted {
		return dom || dow
	}
	return dom && dow
}

func (s Schedule) bit(mask uint64, value int) bool {
	return mask&(1<<uint(value)) != 0
}

func fullMask(low, high int) uint64 {
	var mask uint64
	for value := low; value <= high; value++ {
		mask |= 1 << uint(value)
	}
	return mask
}
