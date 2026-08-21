package model

// The backup document: everything guard was configured with, as one file.
//
// A backup is guard's *configuration* — the machines, the commands, the cloud
// accounts, the saved views, the alert rules, the secrets, the members and the
// stored environment — and never its telemetry. Logs, traces and metrics are the
// part that grows without bound and the part that is reproduced by the next
// minute of ingest; a backup carrying them would be a database copy wearing a
// different extension, and nobody would take one.
//
// The shape is a table per section rather than a struct per feature, and that is
// deliberate: a domain-shaped document has to be edited every time a column is
// added, and the first time somebody forgets, a restore quietly drops the column
// they just introduced. Here the columns come from the database at both ends and
// are matched by name, so a backup taken by an older guard restores into a newer
// one with the new columns left at their defaults, and a column guard no longer
// has is reported rather than fatal.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
)

// BackupFormat is the document version. It is written by the exporter and
// checked by the importer, which refuses anything higher: a file from a newer
// guard may say things this one would silently ignore.
const BackupFormat = 1

// How the sealed values travelled.
const (
	// BackupSecretsPlain: the credentials are in the file as themselves. This
	// is what a backup with no passphrase is, and it is the only way a restore
	// can be a restore — an instance whose SSH passwords and API keys all came
	// back empty is not the instance the file described, and the dashboard's
	// way of saying so is a page of "no stored key" three weeks later.
	//
	// The consequence is the whole of the warning on the page: **the file is
	// the credentials**. Anybody holding it holds them, which is exactly what
	// the passphrase below is for.
	BackupSecretsPlain = "plaintext"
	// BackupSecretsPassphrase: sealed values were opened with this instance's
	// key and re-sealed under a key derived from a passphrase. The file is
	// portable to any guard, and worth nothing without the passphrase.
	BackupSecretsPassphrase = "passphrase"
	// BackupSecretsOmitted is only ever read, never written: the first version
	// of this left the credentials out, and those files still restore — with
	// every credential blank, which is what they actually carry.
	BackupSecretsOmitted = "omitted"
)

// Backup is the whole file.
type Backup struct {
	Format    int    `json:"format"`
	Guard     string `json:"guard_version"`
	CreatedNs int64  `json:"created_ns"`
	// Secrets is one of the two constants above.
	Secrets string `json:"secrets"`
	// KDF is present exactly when Secrets is BackupSecretsPassphrase.
	KDF *BackupKDF `json:"kdf,omitempty"`
	// Notes is anything about this file somebody has to know, in words —
	// today, a value that could not be read on the way out. A file quietly
	// missing the section somebody most wanted is the failure this exists to
	// prevent.
	Notes  []string      `json:"notes,omitempty"`
	Tables []BackupTable `json:"tables"`
}

// BackupKDF is how the passphrase becomes a key, written down so the importer
// does not have to agree with the exporter by convention.
type BackupKDF struct {
	Algorithm string `json:"algorithm"`
	Salt      string `json:"salt"`
	N         int    `json:"n"`
	R         int    `json:"r"`
	P         int    `json:"p"`
	// Check is a known plaintext sealed with the derived key. It is the reason
	// a wrong passphrase is a sentence on the page rather than a restore that
	// half-works: it is opened before anything is written, and before the file
	// is even inspected for values that might happen to decrypt.
	Check string `json:"check"`
}

// BackupTable is one table, with its columns named once and its rows as arrays.
// Named columns and positional rows, because a backup of a thousand secrets
// should not repeat every column name a thousand times.
type BackupTable struct {
	Name    string          `json:"name"`
	Label   string          `json:"label"`
	Group   string          `json:"group"`
	Columns []string        `json:"columns"`
	Rows    [][]BackupValue `json:"rows"`
}

// BackupKind is the SQLite storage class of one value.
type BackupKind uint8

const (
	BackupNull BackupKind = iota
	BackupInt
	BackupReal
	BackupText
	BackupBlob
)

// BackupValue is one cell, typed.
//
// It carries its own JSON encoding for two reasons, both of which were bugs in
// the obvious version. A timestamp in nanoseconds does not survive a round trip
// through a JSON number decoded as float64 — 1.7e18 is past the point where
// doubles count in ones — so integers are parsed from their literal text. And a
// BLOB decoded into `any` comes back as a base64 *string*, indistinguishable
// from a text column that happens to hold base64, so blobs are wrapped in an
// object and stay blobs.
type BackupValue struct {
	Kind BackupKind
	Int  int64
	Real float64
	Text string
	Blob []byte
}

// The five constructors, so nothing has to set Kind by hand.
func BackupNullValue() BackupValue          { return BackupValue{Kind: BackupNull} }
func BackupIntValue(v int64) BackupValue    { return BackupValue{Kind: BackupInt, Int: v} }
func BackupRealValue(v float64) BackupValue { return BackupValue{Kind: BackupReal, Real: v} }
func BackupTextValue(v string) BackupValue  { return BackupValue{Kind: BackupText, Text: v} }

// BackupBlobValue keeps nil distinct from empty: a NULL password and a
// zero-length one are different rows, and the difference is "never set" versus
// "set to nothing".
func BackupBlobValue(v []byte) BackupValue {
	if v == nil {
		return BackupValue{Kind: BackupNull}
	}
	return BackupValue{Kind: BackupBlob, Blob: v}
}

// Any is the value as database/sql wants it.
func (v BackupValue) Any() any {
	switch v.Kind {
	case BackupInt:
		return v.Int
	case BackupReal:
		return v.Real
	case BackupText:
		return v.Text
	case BackupBlob:
		return v.Blob
	default:
		return nil
	}
}

// IsNull answers the one question the sealed-column handling asks.
func (v BackupValue) IsNull() bool { return v.Kind == BackupNull }

func (v BackupValue) MarshalJSON() ([]byte, error) {
	switch v.Kind {
	case BackupInt:
		return []byte(strconv.FormatInt(v.Int, 10)), nil
	case BackupReal:
		if math.IsInf(v.Real, 0) || math.IsNaN(v.Real) {
			// Neither is representable in JSON, and neither is a number any
			// column here should hold; null is the honest answer.
			return []byte("null"), nil
		}
		text := strconv.FormatFloat(v.Real, 'g', -1, 64)
		// A REAL that happens to be whole formats as "2", which would read back
		// as an integer. The decimal point is what keeps the storage class.
		if !strings.ContainsAny(text, ".eE") {
			text += ".0"
		}
		return []byte(text), nil
	case BackupText:
		return json.Marshal(v.Text)
	case BackupBlob:
		return json.Marshal(struct {
			B64 string `json:"b64"`
		}{base64.StdEncoding.EncodeToString(v.Blob)})
	default:
		return []byte("null"), nil
	}
}

func (v *BackupValue) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return errors.New("backup: empty value")
	}
	switch raw[0] {
	case 'n':
		*v = BackupValue{Kind: BackupNull}
		return nil
	case '"':
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		*v = BackupTextValue(text)
		return nil
	case '{':
		var wrapper struct {
			B64 string `json:"b64"`
		}
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			return err
		}
		blob, err := base64.StdEncoding.DecodeString(wrapper.B64)
		if err != nil {
			return err
		}
		if blob == nil {
			blob = []byte{}
		}
		*v = BackupValue{Kind: BackupBlob, Blob: blob}
		return nil
	case 't', 'f':
		// Nothing guard writes is a JSON boolean, but a file edited by hand
		// might be, and 1/0 is what SQLite would have stored anyway.
		var flag bool
		if err := json.Unmarshal(raw, &flag); err != nil {
			return err
		}
		if flag {
			*v = BackupIntValue(1)
		} else {
			*v = BackupIntValue(0)
		}
		return nil
	}
	text := string(raw)
	if !strings.ContainsAny(text, ".eE") {
		if number, err := strconv.ParseInt(text, 10, 64); err == nil {
			*v = BackupIntValue(number)
			return nil
		}
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return errors.New("backup: " + text + " is not a value")
	}
	*v = BackupRealValue(number)
	return nil
}

// BackupSummary is what the page draws before anybody presses anything: what a
// backup taken now would contain, section by section.
type BackupSummary struct {
	Format int                  `json:"format"`
	Guard  string               `json:"guard_version"`
	Tables []BackupTableSummary `json:"tables"`
	Sealed int                  `json:"sealed"`
	// Excluded names the tables a backup leaves behind, so the page can say so
	// rather than leaving somebody to wonder whether their logs are in there.
	Excluded []string `json:"excluded"`
}

// BackupTableSummary is one section's line on that page.
type BackupTableSummary struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Group  string `json:"group"`
	Rows   int    `json:"rows"`
	Sealed int    `json:"sealed"`
}

// RestoreReport is what came back from a restore. Every table is named with
// what it took, because "restored" with no numbers is a word rather than an
// answer.
type RestoreReport struct {
	Guard     string         `json:"guard_version"`
	CreatedNs int64          `json:"created_ns"`
	Tables    []RestoreTable `json:"tables"`
	Rows      int            `json:"rows"`
	// Sealed is how many credentials were re-sealed with this instance's key,
	// and Blank how many came back empty because the file carried none.
	Sealed   int      `json:"sealed"`
	Blank    int      `json:"blank"`
	Warnings []string `json:"warnings"`
	// Restart says the file carried stored configuration, which a running
	// process took its environment from at startup and will not re-read.
	Restart bool `json:"restart"`
}

// RestoreTable is one table's share of that.
type RestoreTable struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Group   string   `json:"group"`
	Rows    int      `json:"rows"`
	Skipped []string `json:"skipped,omitempty"`
}
