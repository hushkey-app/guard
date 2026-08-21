package telemetry

// Export and restore: guard's configuration as one file, and back again.
//
// The catalogue below is the whole feature. A table in it travels; a table not
// in it never does, and the two lists are written out beside each other so the
// question "is my telemetry in this file" has a visible answer rather than an
// inferred one.
//
// Three rules hold it together:
//
//   - **Columns are matched by name at both ends.** The exporter asks the
//     database what the columns are, the importer asks its own, and only the
//     names in both are written. So a backup from an older guard restores into
//     a newer one with the new columns at their defaults, and a column that has
//     since been dropped is reported rather than fatal. Nothing here has a
//     hardcoded column list to forget to update.
//
//   - **A restore replaces.** Every table in the catalogue is emptied and
//     rewritten inside one transaction, ids and all, because the point of a
//     backup is that the instance afterwards is the instance the file describes.
//     A merge would have to answer "which machine is id 3 on this box" and it
//     would answer it wrongly on the day two instances both had one.
//
//   - **Sealed values are opened on the way out and sealed on the way in.**
//     Ciphertext here is bound to the key beside *this* database, so copying it
//     into a file restores as garbage — the failure showing up later as an SSH
//     login that stopped working rather than as an error. So the export opens
//     every credential and the import seals it again with whatever key the
//     receiving instance has. A passphrase, when given, is what protects them
//     in between; without one the file holds them plainly, and says so, because
//     a backup that leaves the credentials behind is not a backup of a
//     configuration that is mostly credentials.

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"

	"github.com/hushkey-app/guard/internal/build"
	"github.com/hushkey-app/guard/internal/secrets"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// backupTable is one line of the catalogue: what to copy, what to call it, and
// which of its columns hold ciphertext.
type backupTable struct {
	name   string
	group  string
	label  string
	sealed []string
}

// backupTables is what a backup carries, in the order it is written — parents
// before the rows that point at them, so a restore never inserts a command for
// a machine that is not there yet. Deleting walks it backwards.
var backupTables = []backupTable{
	{name: "settings", group: "Storage", label: "Retention policy"},
	{name: "config", group: "Configuration", label: "Stored settings", sealed: []string{"value"}},
	{name: "auth_members", group: "Access", label: "Members"},
	{name: "webhooks", group: "Alerts", label: "Destinations", sealed: []string{"token"}},
	{name: "provider_accounts", group: "Cloud", label: "Cloud accounts", sealed: []string{"api_key", "s3_secret"}},
	{name: "cluster_nodes", group: "Cluster", label: "Machines", sealed: []string{"ssh_password"}},
	{name: "cluster_actions", group: "Cluster", label: "Commands"},
	{name: "cluster_assignments", group: "Cluster", label: "Service placements"},
	{name: "cluster_snapshots", group: "Cluster", label: "Snapshots"},
	{name: "cluster_env", group: "Cluster", label: "Machine environments", sealed: []string{"value"}},
	{name: "cluster_env_state", group: "Cluster", label: "Environment state"},
	{name: "cluster_monitors", group: "Alerts", label: "Machine rules"},
	{name: "deploy_groups", group: "Deploys", label: "Deploy groups"},
	{name: "deploy_group_nodes", group: "Deploys", label: "Group membership"},
	{name: "deploy_templates", group: "Deploys", label: "Compose templates"},
	{name: "deploy_state", group: "Deploys", label: "Running versions"},
	{name: "views", group: "Views", label: "Saved views"},
	{name: "secret_workspaces", group: "Secrets", label: "Workspaces"},
	{name: "secret_envs", group: "Secrets", label: "Environments"},
	{name: "secrets", group: "Secrets", label: "Secrets", sealed: []string{"value"}},
	{name: "secret_keys", group: "Secrets", label: "Vault keys"},
}

// backupExcluded is everything a backup deliberately leaves behind, named so
// the page can show it. The first three are the telemetry itself and the rest
// is history and live state: what a machine's disk was at 9am, which command
// ran last night, who is signed in right now. All of it is either regenerated
// within the minute or meaningless on another box.
var backupExcluded = []string{
	"events",
	"event_totals",
	"event_instances",
	"metadata",
	"cluster_checks",
	"cluster_runs",
	"cluster_stats",
	"cluster_monitor_state",
	"deploy_runs",
	"deploy_run_instances",
	"secret_reads",
	"auth_sessions",
	"auth_states",
}

// backupCheckPhrase is the known plaintext sealed into the file's header. It is
// never secret and never needs to be: what it proves is that the passphrase
// typed at a restore derives the same key the export used.
const backupCheckPhrase = "guard-backup"

// scrypt parameters. N is the cost, and 2^15 costs about 32MB and a fraction of
// a second — a passphrase is typed by a person, so the derivation has to be
// expensive enough that a file lifted from a backup drive is not worth grinding
// through a word list. They are written into the file rather than assumed, so
// raising them later does not orphan every backup taken before.
const (
	backupScryptN = 1 << 15
	backupScryptR = 8
	backupScryptP = 1
)

// ErrBackupPassphrase is a wrong or missing passphrase — the one restore
// failure with a specific thing for somebody to do about it.
var ErrBackupPassphrase = errors.New("that passphrase does not open this backup")

// BackupSummary is what a backup taken now would hold, without taking one.
func (s *Store) BackupSummary() (model.BackupSummary, error) {
	summary := model.BackupSummary{
		Format:   model.BackupFormat,
		Guard:    build.Version,
		Tables:   []model.BackupTableSummary{},
		Excluded: backupExcluded,
	}
	for _, table := range backupTables {
		columns, err := tableColumns(s.db, table.name)
		if err != nil {
			return model.BackupSummary{}, err
		}
		if len(columns) == 0 {
			continue
		}
		var rows int
		if err := s.db.QueryRow(`SELECT count(*) FROM "` + table.name + `"`).Scan(&rows); err != nil {
			return model.BackupSummary{}, fmt.Errorf("count %s: %w", table.name, err)
		}
		line := model.BackupTableSummary{Name: table.name, Label: table.label, Group: table.group, Rows: rows}
		for _, column := range table.sealed {
			if !contains(columns, column) {
				continue
			}
			var sealed int
			query := fmt.Sprintf(`SELECT count(*) FROM %q WHERE %q IS NOT NULL AND length(%q) > 0`, table.name, column, column)
			if err := s.db.QueryRow(query).Scan(&sealed); err != nil {
				return model.BackupSummary{}, fmt.Errorf("count %s.%s: %w", table.name, column, err)
			}
			line.Sealed += sealed
		}
		summary.Sealed += line.Sealed
		summary.Tables = append(summary.Tables, line)
	}
	return summary, nil
}

// ExportBackup reads the catalogue into a document.
//
// Every sealed value is opened with this instance's key on the way out, because
// ciphertext in a file is worth nothing anywhere — not even on the machine it
// came from, since the key it is bound to lives beside the *database*, and the
// first thing a restore does is write rows a running guard will try to open.
// What differs is what happens next:
//
//   - **With a passphrase**, the value is sealed again under a key derived from
//     it. The file travels anywhere and is worth nothing without the words.
//   - **Without one**, the value goes in as itself. The file *is* the
//     credentials — which is the only way a restore comes back as the instance
//     that was backed up, and is why the page says so in as many words.
//
// A value that will not open — sealed with a key this instance no longer has —
// is left out with a note rather than failing the export, for the same reason
// every other read of a sealed column skips it: the backup of the other
// ninety-nine machines is still worth having.
func (s *Store) ExportBackup(passphrase string) (model.Backup, error) {
	doc := model.Backup{
		Format:    model.BackupFormat,
		Guard:     build.Version,
		CreatedNs: time.Now().UTC().UnixNano(),
		Secrets:   model.BackupSecretsPlain,
		Tables:    []model.BackupTable{},
	}
	var keeper *secrets.Keeper
	if strings.TrimSpace(passphrase) != "" {
		derived, kdf, err := newBackupKeeper(passphrase)
		if err != nil {
			return model.Backup{}, err
		}
		keeper, doc.KDF, doc.Secrets = derived, kdf, model.BackupSecretsPassphrase
	}
	for _, table := range backupTables {
		columns, required, err := tableColumnInfo(s.db, table.name)
		if err != nil {
			return model.Backup{}, err
		}
		if len(columns) == 0 {
			// A table this build does not have. Nothing to say about it.
			continue
		}
		out := model.BackupTable{Name: table.name, Label: table.label, Group: table.group, Columns: columns, Rows: [][]model.BackupValue{}}
		sealedAt := map[int]bool{}
		for index, column := range columns {
			if contains(table.sealed, column) {
				sealedAt[index] = true
			}
		}
		dropped := 0
		list := make([]string, len(columns))
		for i, column := range columns {
			list[i] = fmt.Sprintf("%q", column)
		}
		rows, err := s.db.Query(fmt.Sprintf(`SELECT %s FROM %q`, strings.Join(list, ", "), table.name))
		if err != nil {
			return model.Backup{}, fmt.Errorf("read %s: %w", table.name, err)
		}
		for rows.Next() {
			cells := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range cells {
				pointers[i] = &cells[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return model.Backup{}, fmt.Errorf("read %s: %w", table.name, err)
			}
			values := make([]model.BackupValue, len(columns))
			skip := false
			for i, cell := range cells {
				value := backupValueOf(cell)
				if !sealedAt[i] {
					values[i] = value
					continue
				}
				values[i] = model.BackupNullValue()
				if value.IsNull() || len(value.Blob) == 0 {
					continue
				}
				// Opened here either way: ciphertext bound to this database's
				// key is worth nothing in a file, even on the machine it came
				// from — the whole point is that the row travels as a value
				// again on the way in.
				plain, err := s.secrets.Open(value.Blob)
				if err != nil {
					slog.Warn("backup: a sealed value could not be read and was left out",
						slog.String("table", table.name), slog.String("column", columns[i]), slog.Any("err", err))
					// A sealed column the table insists on — config.value is
					// the whole of a stored setting — cannot be left blank, so
					// the row goes rather than the value.
					if required[columns[i]] {
						skip = true
						break
					}
					doc.Notes = append(doc.Notes, fmt.Sprintf(
						"%s: a stored credential could not be read with this instance's key and was left out.", table.label))
					continue
				}
				if keeper == nil {
					// No passphrase: the value itself, which is what makes a
					// restore a restore. The page says what that means for the
					// file.
					values[i] = model.BackupTextValue(plain)
					continue
				}
				resealed, err := keeper.Seal(plain)
				if err != nil {
					rows.Close()
					return model.Backup{}, err
				}
				values[i] = model.BackupBlobValue(resealed)
			}
			if skip {
				dropped++
				continue
			}
			out.Rows = append(out.Rows, values)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return model.Backup{}, err
		}
		rows.Close()
		if dropped > 0 {
			doc.Notes = append(doc.Notes, fmt.Sprintf(
				"%s: %d row(s) left out — the stored value could not be read with this instance's key.",
				table.label, dropped))
		}
		doc.Tables = append(doc.Tables, out)
	}
	return doc, nil
}

// RestoreBackup replaces every table in the catalogue with the file's rows.
//
// Everything that can fail is done before anything is written: the format is
// checked, the passphrase is proved against the header, every sealed value is
// opened and re-sealed with this instance's key, and only then does the
// transaction start. A restore that got half way would leave a dashboard whose
// machines are from the file and whose commands are from before it.
func (s *Store) RestoreBackup(doc model.Backup, passphrase string) (model.RestoreReport, error) {
	report := model.RestoreReport{Guard: doc.Guard, CreatedNs: doc.CreatedNs, Tables: []model.RestoreTable{}, Warnings: []string{}}
	if doc.Format < 1 {
		return report, errors.New("this file is not a guard backup")
	}
	if doc.Format > model.BackupFormat {
		return report, fmt.Errorf("this backup was written by a newer guard (format %d, this build reads %d)", doc.Format, model.BackupFormat)
	}
	if len(doc.Tables) == 0 {
		return report, errors.New("this backup has no tables in it")
	}
	var keeper *secrets.Keeper
	if doc.Secrets == model.BackupSecretsPassphrase {
		if strings.TrimSpace(passphrase) == "" {
			return report, ErrBackupPassphrase
		}
		opened, err := openBackupKeeper(doc.KDF, passphrase)
		if err != nil {
			return report, err
		}
		keeper = opened
	}
	if s.secrets.Ephemeral {
		report.Warnings = append(report.Warnings,
			"This instance has no key file, so restored credentials are sealed with a key that dies with the process.")
	}

	// The plan: what each table will actually insert, with the sealed values
	// already re-sealed. Built entirely outside the transaction.
	type plan struct {
		table   backupTable
		columns []string
		rows    [][]model.BackupValue
		skipped []string
		label   string
		group   string
	}
	known := map[string]backupTable{}
	for _, table := range backupTables {
		known[table.name] = table
	}
	plans := []plan{}
	for _, table := range backupTables {
		source := findBackupTable(doc, table.name)
		if source == nil {
			continue
		}
		live, required, err := tableColumnInfo(s.db, table.name)
		if err != nil {
			return report, err
		}
		if len(live) == 0 {
			report.Warnings = append(report.Warnings, "This build has no "+table.name+" table; that section was skipped.")
			continue
		}
		keep := []int{}
		columns := []string{}
		skipped := []string{}
		for index, column := range source.Columns {
			if contains(live, column) {
				keep = append(keep, index)
				columns = append(columns, column)
				continue
			}
			skipped = append(skipped, column)
		}
		if len(columns) == 0 {
			report.Warnings = append(report.Warnings, "No column of "+table.name+" is one this build has; that section was skipped.")
			continue
		}
		rows := make([][]model.BackupValue, 0, len(source.Rows))
		dropped := 0
		for _, row := range source.Rows {
			values := make([]model.BackupValue, 0, len(columns))
			skip := false
			for position, index := range keep {
				if index >= len(row) {
					return report, fmt.Errorf("%s: a row has fewer values than columns", table.name)
				}
				value := row[index]
				if !contains(table.sealed, columns[position]) {
					values = append(values, value)
					continue
				}
				if value.IsNull() || (value.Kind == model.BackupBlob && len(value.Blob) == 0) ||
					(value.Kind == model.BackupText && value.Text == "") {
					// Nothing came for a column that will not hold nothing:
					// the row is not restorable, and half of it would be worse
					// than none of it.
					if required[columns[position]] {
						skip = true
						break
					}
					report.Blank++
					values = append(values, model.BackupNullValue())
					continue
				}
				// The kind is the answer, not the header: a text cell is the
				// value itself, a blob is sealed under the passphrase. So a
				// file that says one thing and carries the other still lands
				// correctly, and there is no mode to keep in step.
				plain := value.Text
				if value.Kind == model.BackupBlob {
					opened, err := keeperOpen(keeper, value.Blob)
					if err != nil {
						// The header already proved the passphrase, so this is
						// a damaged file rather than the wrong words. Left
						// blank, and said out loud: a credential nobody knows
						// is missing is worse than one somebody is told to set
						// again.
						report.Warnings = append(report.Warnings,
							fmt.Sprintf("A sealed value in %s could not be read and was restored empty.", table.name))
						report.Blank++
						values = append(values, model.BackupNullValue())
						continue
					}
					plain = opened
				}
				resealed, err := s.secrets.Seal(plain)
				if err != nil {
					return report, err
				}
				report.Sealed++
				values = append(values, model.BackupBlobValue(resealed))
			}
			if skip {
				dropped++
				continue
			}
			rows = append(rows, values)
		}
		if dropped > 0 {
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"%s: %d row(s) carried no value and were left out.", table.label, dropped))
		}
		plans = append(plans, plan{table: table, columns: columns, rows: rows, skipped: skipped, label: table.label, group: table.group})
	}
	for _, source := range doc.Tables {
		if _, ok := known[source.Name]; !ok {
			report.Warnings = append(report.Warnings, "This build does not know the "+source.Name+" section; it was skipped.")
		}
	}
	if len(plans) == 0 {
		return report, errors.New("nothing in this backup is a section this build restores")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	// The catalogue is written parents-first, but a delete of cluster_nodes
	// cascades into rows a later insert is about to write. Deferring the
	// constraint checks to the commit means the order inside the transaction is
	// ours to choose and the answer is still checked at the end.
	if _, err := tx.Exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
		return report, err
	}
	for index := len(plans) - 1; index >= 0; index-- {
		if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %q`, plans[index].table.name)); err != nil {
			return report, fmt.Errorf("clear %s: %w", plans[index].table.name, err)
		}
	}
	for _, item := range plans {
		names := make([]string, len(item.columns))
		holes := make([]string, len(item.columns))
		for i, column := range item.columns {
			names[i] = fmt.Sprintf("%q", column)
			holes[i] = "?"
		}
		statement, err := tx.Prepare(fmt.Sprintf(`INSERT INTO %q (%s) VALUES (%s)`,
			item.table.name, strings.Join(names, ", "), strings.Join(holes, ", ")))
		if err != nil {
			return report, fmt.Errorf("write %s: %w", item.table.name, err)
		}
		for _, row := range item.rows {
			args := make([]any, len(row))
			for i, value := range row {
				args[i] = value.Any()
			}
			if _, err := statement.Exec(args...); err != nil {
				statement.Close()
				return report, fmt.Errorf("write %s: %w", item.table.name, err)
			}
		}
		statement.Close()
		report.Rows += len(item.rows)
		report.Tables = append(report.Tables, model.RestoreTable{
			Name: item.table.name, Label: item.label, Group: item.group,
			Rows: len(item.rows), Skipped: item.skipped,
		})
		if item.table.name == "config" && len(item.rows) > 0 {
			report.Restart = true
		}
	}
	// What an alert has already said is about the machines this instance was
	// watching a moment ago, not the ones it is watching now. Both watchers
	// start again from "nothing has been reported", so the next pass fires or
	// stays quiet on what it can actually measure — and no receiver is closed
	// out of an incident guard never observed.
	if _, err := tx.Exec(`DELETE FROM cluster_monitor_state`); err != nil {
		return report, err
	}
	if _, err := tx.Exec(`UPDATE views SET alert_firing = 0, alert_since_ns = 0, alert_alerted_ns = 0, alert_value = 0, alert_series = ''`); err != nil {
		return report, err
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("restore: %w", err)
	}
	// Placements just changed under the cached map of which service runs where.
	s.topology.invalidate()
	slog.Info("configuration restored from a backup",
		slog.Int("rows", report.Rows), slog.Int("sealed", report.Sealed), slog.String("from", doc.Guard))
	return report, nil
}

// newBackupKeeper derives a key from a passphrase and writes down how, together
// with the sealed phrase that proves the same passphrase later.
func newBackupKeeper(passphrase string) (*secrets.Keeper, *model.BackupKDF, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	key, err := scrypt.Key([]byte(passphrase), salt, backupScryptN, backupScryptR, backupScryptP, 32)
	if err != nil {
		return nil, nil, err
	}
	keeper, err := secrets.New(key)
	if err != nil {
		return nil, nil, err
	}
	check, err := keeper.Seal(backupCheckPhrase)
	if err != nil {
		return nil, nil, err
	}
	return keeper, &model.BackupKDF{
		Algorithm: "scrypt",
		Salt:      encodeBase64(salt),
		N:         backupScryptN,
		R:         backupScryptR,
		P:         backupScryptP,
		Check:     encodeBase64(check),
	}, nil
}

// openBackupKeeper rebuilds that key from the file's parameters, and refuses
// before anything is read if the passphrase is not the one used.
func openBackupKeeper(kdf *model.BackupKDF, passphrase string) (*secrets.Keeper, error) {
	if kdf == nil {
		return nil, errors.New("this backup says it holds sealed values but does not say how they were sealed")
	}
	if kdf.Algorithm != "" && kdf.Algorithm != "scrypt" {
		return nil, fmt.Errorf("this backup was sealed with %s, which this build does not know", kdf.Algorithm)
	}
	salt, err := decodeBase64(kdf.Salt)
	if err != nil || len(salt) == 0 {
		return nil, errors.New("this backup's header is damaged")
	}
	n, r, p := kdf.N, kdf.R, kdf.P
	if n < 2 || r < 1 || p < 1 {
		return nil, errors.New("this backup's header is damaged")
	}
	key, err := scrypt.Key([]byte(passphrase), salt, n, r, p, 32)
	if err != nil {
		return nil, err
	}
	keeper, err := secrets.New(key)
	if err != nil {
		return nil, err
	}
	check, err := decodeBase64(kdf.Check)
	if err != nil || len(check) == 0 {
		return nil, errors.New("this backup's header is damaged")
	}
	phrase, err := keeper.Open(check)
	if err != nil || phrase != backupCheckPhrase {
		return nil, ErrBackupPassphrase
	}
	return keeper, nil
}

func keeperOpen(keeper *secrets.Keeper, sealed []byte) (string, error) {
	if keeper == nil {
		return "", errors.New("no key for this value")
	}
	return keeper.Open(sealed)
}

func findBackupTable(doc model.Backup, name string) *model.BackupTable {
	for index := range doc.Tables {
		if doc.Tables[index].Name == name {
			return &doc.Tables[index]
		}
	}
	return nil
}

// tableColumns is what the database says this table has, or nothing at all if
// there is no such table. table_info rather than table_xinfo: a generated
// column is computed from the others and cannot be inserted into.
func tableColumns(db *sql.DB, table string) ([]string, error) {
	columns, _, err := tableColumnInfo(db, table)
	return columns, err
}

// tableColumnInfo adds which of them refuse a NULL, which is the difference
// between "this credential was left out" and a row that cannot be written at
// all — config.value is the sealed column that is also the whole row.
func tableColumnInfo(db *sql.DB, table string) ([]string, map[string]bool, error) {
	rows, err := db.Query(`SELECT name, "notnull" FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s columns: %w", table, err)
	}
	defer rows.Close()
	columns := []string{}
	required := map[string]bool{}
	for rows.Next() {
		var name string
		var notNull int
		if err := rows.Scan(&name, &notNull); err != nil {
			return nil, nil, err
		}
		columns = append(columns, name)
		required[name] = notNull != 0
	}
	return columns, required, rows.Err()
}

// backupValueOf turns what the driver handed back into a typed cell.
func backupValueOf(cell any) model.BackupValue {
	switch value := cell.(type) {
	case nil:
		return model.BackupNullValue()
	case int64:
		return model.BackupIntValue(value)
	case int:
		return model.BackupIntValue(int64(value))
	case float64:
		return model.BackupRealValue(value)
	case bool:
		if value {
			return model.BackupIntValue(1)
		}
		return model.BackupIntValue(0)
	case string:
		return model.BackupTextValue(value)
	case []byte:
		return model.BackupBlobValue(value)
	case time.Time:
		return model.BackupIntValue(value.UTC().UnixNano())
	default:
		return model.BackupTextValue(fmt.Sprint(value))
	}
}

func encodeBase64(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }

func decodeBase64(text string) ([]byte, error) { return base64.StdEncoding.DecodeString(text) }

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
