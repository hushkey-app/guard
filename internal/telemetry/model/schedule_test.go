package model

import (
	"testing"
	"time"
)

func at(spec string) time.Time {
	t, err := time.Parse(time.RFC3339, spec)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestParseScheduleRejectsNonsense(t *testing.T) {
	for _, spec := range []string{
		"0 */6 * *",        // four fields
		"60 * * * *",       // no minute 60
		"* * * * 9",        // no weekday 9
		"@every 5s",        // under the floor
		"@every 60d",       // over the ceiling
		"@every fortnight", // not a duration
		"0 0 * * funday",
	} {
		if _, err := ParseSchedule(spec); err == nil {
			t.Errorf("%q was accepted", spec)
		}
	}
}

func TestParseScheduleEmptyIsNotASchedule(t *testing.T) {
	schedule, err := ParseSchedule("  ")
	if err != nil {
		t.Fatalf("empty schedule: %v", err)
	}
	if schedule.Set() {
		t.Fatal("an empty expression is an action nobody scheduled")
	}
	if !schedule.Next(time.Now()).IsZero() {
		t.Fatal("a schedule that is not set has no next run")
	}
}

func TestNextRunsEverySixHours(t *testing.T) {
	schedule, err := ParseSchedule("0 */6 * * *")
	if err != nil {
		t.Fatal(err)
	}
	next := schedule.Next(at("2026-08-15T01:13:00Z"))
	if want := at("2026-08-15T06:00:00Z"); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
	// And the one after it, from its own fire: six hours, not a minute past.
	if got, want := schedule.Next(next), at("2026-08-15T12:00:00Z"); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
}

func TestNextIsStrictlyAfter(t *testing.T) {
	schedule, _ := ParseSchedule("0 * * * *")
	// Standing exactly on a fire returns the next one, never the same one
	// again — otherwise a job fires in a loop for the whole minute it is due.
	got := schedule.Next(at("2026-08-15T09:00:00Z"))
	if want := at("2026-08-15T10:00:00Z"); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
}

func TestNextCrossesMonthsAndYears(t *testing.T) {
	schedule, err := ParseSchedule("30 2 1 jan *")
	if err != nil {
		t.Fatal(err)
	}
	got := schedule.Next(at("2026-08-15T09:00:00Z"))
	if want := at("2027-01-01T02:30:00Z"); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
}

func TestNextTakesEitherDayWhenBothAreRestricted(t *testing.T) {
	// The classic rule: "the 1st, and every Monday", not "the 1st when it is a
	// Monday". 2026-08-15 is a Saturday; the next Monday is the 17th.
	schedule, err := ParseSchedule("0 0 1 * mon")
	if err != nil {
		t.Fatal(err)
	}
	got := schedule.Next(at("2026-08-15T09:00:00Z"))
	if want := at("2026-08-17T00:00:00Z"); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
}

func TestEveryIsAPeriodFromTheLastRun(t *testing.T) {
	schedule, err := ParseSchedule("@every 6h")
	if err != nil {
		t.Fatal(err)
	}
	if schedule.Every() != 6*time.Hour {
		t.Fatalf("every = %s", schedule.Every())
	}
	// A run that finished at 01:13 waits until 07:13 — a period rather than a
	// clock, so a long job does not shorten the gap after it.
	got := schedule.Next(at("2026-08-15T01:13:00Z"))
	if want := at("2026-08-15T07:13:00Z"); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
}

func TestShorthands(t *testing.T) {
	schedule, err := ParseSchedule("@daily")
	if err != nil {
		t.Fatal(err)
	}
	got := schedule.Next(at("2026-08-15T09:00:00Z"))
	if want := at("2026-08-16T00:00:00Z"); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
}

func TestNextRunAnchorsOnTheLastRun(t *testing.T) {
	action := NodeAction{Schedule: "@every 6h", LastRunAt: at("2026-08-15T01:00:00Z")}
	if got, want := action.NextRun(at("2026-08-15T03:00:00Z")), at("2026-08-15T07:00:00Z"); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
	// Never run: due one period from now, not immediately. A schedule typed
	// into a form should not fire while the form is still open.
	fresh := NodeAction{Schedule: "@every 6h"}
	if got, want := fresh.NextRun(at("2026-08-15T03:00:00Z")), at("2026-08-15T09:00:00Z"); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
	// A missed window is due now rather than skipped: guard was down, and the
	// dump still has not happened.
	missed := NodeAction{Schedule: "@every 6h", LastRunAt: at("2026-08-14T01:00:00Z")}
	if due := missed.NextRun(at("2026-08-15T03:00:00Z")); !due.Before(at("2026-08-15T03:00:00Z")) {
		t.Fatalf("a missed run should be overdue, got %s", due)
	}
}

func TestStaleReadsTheLastSuccessNotTheLastRun(t *testing.T) {
	now := at("2026-08-15T09:00:00Z")
	// Failing on the dot every six hours: a very recent last run, and nothing
	// that worked since yesterday. This is the case the watch exists for.
	action := NodeAction{
		StaleAfterSeconds: int((7 * time.Hour).Seconds()),
		LastRunAt:         now.Add(-5 * time.Minute),
		LastOKAt:          now.Add(-9 * time.Hour),
	}
	stale, since := action.Stale(now)
	if !stale {
		t.Fatal("nine hours without a success is stale against a seven hour budget")
	}
	if !since.Equal(action.LastOKAt) {
		t.Fatalf("since = %s, want the last success", since)
	}
	action.LastOKAt = now.Add(-time.Hour)
	if stale, _ := action.Stale(now); stale {
		t.Fatal("an hour ago is inside the budget")
	}
}

func TestStaleNeedsAThreshold(t *testing.T) {
	now := at("2026-08-15T09:00:00Z")
	action := NodeAction{LastOKAt: now.Add(-30 * 24 * time.Hour)}
	if stale, _ := action.Stale(now); stale {
		t.Fatal("nobody asked to be told about this action")
	}
}

func TestStaleOnAnActionThatHasNeverSucceededMeasuresFromCreation(t *testing.T) {
	now := at("2026-08-15T09:00:00Z")
	action := NodeAction{
		StaleAfterSeconds: int((7 * time.Hour).Seconds()),
		CreatedAt:         now.Add(-8 * time.Hour),
	}
	stale, since := action.Stale(now)
	if !stale {
		t.Fatal("a job that has never worked is the loudest case, not the quietest")
	}
	if !since.Equal(action.CreatedAt) {
		t.Fatalf("since = %s, want the creation time", since)
	}
	// Freshly created: nothing to report yet.
	action.CreatedAt = now.Add(-time.Hour)
	if stale, _ := action.Stale(now); stale {
		t.Fatal("an action created an hour ago has not missed a seven hour budget")
	}
}

func TestValidateRejectsAnUnreadableSchedule(t *testing.T) {
	action := NodeAction{Name: "Dump", Command: "pg_dump", Schedule: "every 6 hours"}
	if err := action.Validate(); err == nil {
		t.Fatal("a schedule guard cannot read is one that silently never fires")
	}
	action.Schedule = "0 */6 * * *"
	if err := action.Validate(); err != nil {
		t.Fatalf("valid action: %v", err)
	}
	action.StaleAfterSeconds = 30
	if err := action.Validate(); err == nil {
		t.Fatal("a half-minute alert threshold is a false alarm generator")
	}
}
