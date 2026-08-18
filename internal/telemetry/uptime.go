package telemetry

// The public status page's data: one row per machine per day.
//
// It exists because cluster_checks cannot answer the question. That table keeps
// exactly one day (checkRetention), and deliberately — the cadence is per node
// and defaults to three seconds, so a single machine is 28,800 rows a day and a
// quarter of them across twenty machines would be fifty million rows in service
// of a bar chart.
//
// So the rollup is written as the checks arrive: two integers per machine per
// day, upserted on the row that is already there. Ninety days of twenty
// machines is 1,800 rows, which is smaller than an hour of the raw table.
//
// The consequence worth stating: a day's figure is only ever as good as the
// checks that ran. A machine paused for a week has no rows for that week, and
// the page draws those days as "no data" rather than as an outage — which is
// the truth. Guard does not know what a machine it was not watching was doing.

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// uptimeDayRetention is how much history the status page can ever show. It is
// generous because the rows are tiny: at one per machine per day, a year of
// twenty machines is 7,300 rows.
const uptimeDayRetention = 400

// StatusDays is the window the public page draws, and the number the summary
// percentage is computed over.
const StatusDays = 90

func migrateUptime(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS cluster_uptime_days (
  node_id INTEGER NOT NULL,
  day     INTEGER NOT NULL,
  checks  INTEGER NOT NULL DEFAULT 0,
  ok      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(node_id, day)
);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate uptime days: %w", err)
	}
	return nil
}

// epochDay is the day a moment falls in, counted in whole UTC days.
//
// UTC rather than the server's zone: the page is public, its readers are not in
// one place, and a bar that means "Tuesday" only to whoever provisioned the box
// is a bar that means nothing to everyone else.
func epochDay(at time.Time) int64 { return at.UTC().Unix() / 86400 }

// recordUptimeDay folds one check into its day.
//
// Called from RecordCheck, in the same breath as the raw row, so the rollup can
// never disagree with the history it summarises — a nightly job would leave a
// window where the two answer differently, and the day it failed would look
// exactly like a day with no checks.
func (s *Store) recordUptimeDay(nodeID int64, at time.Time, ok bool) error {
	success := 0
	if ok {
		success = 1
	}
	_, err := s.db.Exec(`INSERT INTO cluster_uptime_days(node_id,day,checks,ok) VALUES(?,?,1,?)
ON CONFLICT(node_id,day) DO UPDATE SET checks = checks + 1, ok = ok + ?`,
		nodeID, epochDay(at), success, success)
	return err
}

// pruneUptimeDays drops history no page can ask for.
func (s *Store) pruneUptimeDays() error {
	_, err := s.db.Exec(`DELETE FROM cluster_uptime_days WHERE day < ?`,
		epochDay(time.Now())-uptimeDayRetention)
	return err
}

// PublicStatus is everything the status page shows, and nothing else.
//
// This is the one read in guard that answers an unauthenticated request, so the
// shape of it is the security boundary. It selects the machines that were
// marked public, one at a time, by name — and it returns the public name, never
// the machine's own: the point of a nickname is that "PACK-POSTGRES-VPS-MAIN"
// is an internal fact and "Database" is what a customer should read.
//
// Nothing else about a machine leaves through here. No address, no group, no
// tags, no error text, no status code, no latency. A status page that leaked
// "connection refused at 10.19.96.4:5432" would be an inventory of the private
// network, published, in service of a green dot.
func (s *Store) PublicStatus() (model.PublicStatus, error) {
	rows, err := s.db.Query(`SELECT id, COALESCE(NULLIF(public_name,''), name)
FROM cluster_nodes WHERE public = 1 AND enabled = 1 ORDER BY name`)
	if err != nil {
		return model.PublicStatus{}, err
	}
	defer rows.Close()

	type entry struct {
		id   int64
		name string
	}
	var wanted []entry
	for rows.Next() {
		var item entry
		if err := rows.Scan(&item.id, &item.name); err != nil {
			return model.PublicStatus{}, err
		}
		wanted = append(wanted, item)
	}
	if err := rows.Err(); err != nil {
		return model.PublicStatus{}, err
	}

	status := model.PublicStatus{
		Days:     StatusDays,
		Services: make([]model.PublicService, 0, len(wanted)),
		AsOf:     time.Now().UTC(),
	}
	today := epochDay(time.Now())
	first := today - int64(StatusDays) + 1

	for _, item := range wanted {
		service := model.PublicService{
			Name: item.name,
			Days: make([]model.PublicDay, 0, StatusDays),
		}

		history, err := s.db.Query(`SELECT day, checks, ok FROM cluster_uptime_days
WHERE node_id = ? AND day >= ? ORDER BY day`, item.id, first)
		if err != nil {
			return model.PublicStatus{}, err
		}
		byDay := map[int64][2]int64{}
		for history.Next() {
			var day, checks, ok int64
			if err := history.Scan(&day, &checks, &ok); err != nil {
				history.Close()
				return model.PublicStatus{}, err
			}
			byDay[day] = [2]int64{checks, ok}
		}
		history.Close()
		if err := history.Err(); err != nil {
			return model.PublicStatus{}, err
		}

		var totalChecks, totalOK int64
		for day := first; day <= today; day++ {
			counts, watched := byDay[day]
			entry := model.PublicDay{
				Date: time.Unix(day*86400, 0).UTC().Format("2006-01-02"),
			}
			if watched && counts[0] > 0 {
				entry.Checks = counts[0]
				entry.OK = counts[1]
				entry.Uptime = float64(counts[1]) / float64(counts[0]) * 100
				totalChecks += counts[0]
				totalOK += counts[1]
			}
			service.Days = append(service.Days, entry)
		}

		// The headline percentage is over the checks that ran, not over the
		// days in the window. A machine added on Friday reads 100% for its two
		// days rather than 2% for the quarter it did not exist in.
		if totalChecks > 0 {
			service.Uptime = float64(totalOK) / float64(totalChecks) * 100
			service.Watched = true
		}

		// Present state comes from the latest check, which is a different
		// question from the day's average: a machine can be down right now on a
		// day that is 99% green, and the dot has to say "down".
		//
		// Only `ok` is read. The status code, the latency and the error text
		// all exist on that row and none of them belong on a public page.
		var latest sql.NullBool
		err = s.db.QueryRow(`SELECT ok FROM cluster_checks WHERE node_id = ?
ORDER BY checked_at_ns DESC LIMIT 1`, item.id).Scan(&latest)
		switch {
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return model.PublicStatus{}, err
		case !latest.Valid:
			service.State = "unknown"
		case latest.Bool:
			service.State = "operational"
		default:
			service.State = "down"
		}

		status.Services = append(status.Services, service)
	}

	// One headline for the whole page, and it is the worst of its parts. A page
	// saying "All Systems Operational" above a red row is the one thing a status
	// page must never do.
	status.State = "operational"
	for _, service := range status.Services {
		if service.State == "down" {
			status.State = "down"
			break
		}
		if service.State == "unknown" {
			status.State = "partial"
		}
	}
	if len(status.Services) == 0 {
		status.State = "unknown"
	}
	return status, nil
}
