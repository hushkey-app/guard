package model

import "testing"

func TestParseEnvReadsWhatPeopleActuallyPaste(t *testing.T) {
	text := `# the database
DATABASE_URL=postgres://localhost/app
export REDIS_URL=redis://localhost:6379   # with a trailing comment
QUOTED="a value with spaces"
SINGLE='no \escapes here'
EMPTY=
EQUALS=a=b=c
NEWLINES="first\nsecond"
`
	pairs, skipped := ParseEnv(text)
	if len(skipped) != 0 {
		t.Fatalf("skipped %+v", skipped)
	}
	want := map[string]string{
		"DATABASE_URL": "postgres://localhost/app",
		"REDIS_URL":    "redis://localhost:6379",
		"QUOTED":       "a value with spaces",
		"SINGLE":       `no \escapes here`,
		"EMPTY":        "",
		"EQUALS":       "a=b=c",
		"NEWLINES":     "first\nsecond",
	}
	if len(pairs) != len(want) {
		t.Fatalf("read %+v", pairs)
	}
	for _, pair := range pairs {
		if value, ok := want[pair.Key]; !ok || value != pair.Value {
			t.Fatalf("%s = %q, wanted %q", pair.Key, pair.Value, value)
		}
	}
}

// The case a line-at-a-time parser gets wrong, and the reason this one is not
// one: a PEM key in a .env file is a single double-quoted value over twenty
// lines, and a parser that splits on newlines stores the first one.
func TestParseEnvKeepsAMultiLineQuotedValueWhole(t *testing.T) {
	text := "NAME=before\nPRIVATE_KEY=\"-----BEGIN KEY-----\nline two\nline three\n-----END KEY-----\"\nAFTER=yes\n"
	pairs, skipped := ParseEnv(text)
	if len(skipped) != 0 {
		t.Fatalf("skipped %+v", skipped)
	}
	if len(pairs) != 3 {
		t.Fatalf("read %+v", pairs)
	}
	want := "-----BEGIN KEY-----\nline two\nline three\n-----END KEY-----"
	if pairs[1].Key != "PRIVATE_KEY" || pairs[1].Value != want {
		t.Fatalf("the key came out as %q", pairs[1].Value)
	}
	if pairs[2].Key != "AFTER" || pairs[2].Value != "yes" {
		t.Fatalf("the line after the block was lost: %+v", pairs[2])
	}
}

func TestParseEnvNamesWhatItCouldNotRead(t *testing.T) {
	text := "GOOD=1\nnot a pair\n2START=x\nwith space=y\nOPEN=\"never closed\n"
	pairs, skipped := ParseEnv(text)
	if len(pairs) != 1 || pairs[0].Key != "GOOD" {
		t.Fatalf("read %+v", pairs)
	}
	if len(skipped) != 4 {
		t.Fatalf("skipped %+v", skipped)
	}
	// The line number is the point of the report: "4 lines skipped" sends
	// somebody back to a hundred-line file to find out which.
	lines := []int{2, 3, 4, 5}
	for i, skip := range skipped {
		if skip.Line != lines[i] || skip.Reason == "" {
			t.Fatalf("skip %d is %+v", i, skip)
		}
	}
}

func TestFormatEnvRoundTrips(t *testing.T) {
	secrets := []Secret{
		{Key: "PLAIN", Value: "value"},
		{Key: "SPACED", Value: "two words"},
		{Key: "QUOTES", Value: `he said "hi"`},
		{Key: "MULTI", Value: "first\nsecond"},
		{Key: "SLASH", Value: `C:\path\to`},
	}
	pairs, skipped := ParseEnv(FormatEnv(secrets))
	if len(skipped) != 0 {
		t.Fatalf("its own output would not parse: %+v", skipped)
	}
	if len(pairs) != len(secrets) {
		t.Fatalf("read back %+v", pairs)
	}
	for i, pair := range pairs {
		if pair.Key != secrets[i].Key || pair.Value != secrets[i].Value {
			t.Fatalf("%s came back as %q", secrets[i].Key, pair.Value)
		}
	}
}

// The whole point of Compare is that a colour and a button read the same
// answer, so what is asserted is the state of every cell rather than a count.
func TestCompareStatesTheRowNotTheValue(t *testing.T) {
	envs := []Env{{ID: 1, Name: "staging"}, {ID: 2, Name: "production"}, {ID: 3, Name: "develop"}}
	values := [][]Secret{
		{{ID: 11, Key: "DATABASE_URL", Value: "a"}, {ID: 12, Key: "LOG_LEVEL", Value: "info"}, {ID: 13, Key: "ONLY_STAGING", Value: "x"}},
		{{ID: 21, Key: "DATABASE_URL", Value: "b"}, {ID: 22, Key: "LOG_LEVEL", Value: "info"}},
		{{ID: 31, Key: "DATABASE_URL", Value: "a"}, {ID: 32, Key: "LOG_LEVEL", Value: "info"}, {ID: 33, Key: "ONLY_STAGING", Value: "x"}},
	}
	got := Compare(envs, values)

	want := map[string][]string{
		// Two agree and one does not: nobody is "same" here, because same is
		// about the row.
		"DATABASE_URL": {CompareDifferent, CompareDifferent, CompareDifferent},
		"LOG_LEVEL":    {CompareSame, CompareSame, CompareSame},
		// Absent from one is red there and green in the two that agree — the
		// pair genuinely does match, and saying otherwise would send somebody
		// looking for a difference that is not there.
		"ONLY_STAGING": {CompareSame, CompareMissing, CompareSame},
	}
	if len(got.Rows) != len(want) {
		t.Fatalf("rows %+v", got.Rows)
	}
	for _, row := range got.Rows {
		states := want[row.Key]
		if states == nil {
			t.Fatalf("unexpected row %q", row.Key)
		}
		for i, state := range states {
			if row.Cells[i].State != state {
				t.Fatalf("%s in %s: %s, wanted %s", row.Key, envs[i].Name, row.Cells[i].State, state)
			}
		}
	}
	if got.Same != 1 || got.Different != 1 || got.Missing != 1 {
		t.Fatalf("counts: %d same, %d different, %d missing", got.Same, got.Different, got.Missing)
	}
	// A delete has to know which row it is deleting, and the only place that
	// id can come from is here.
	for _, row := range got.Rows {
		for i, cell := range row.Cells {
			if cell.Present == (cell.SecretID == 0) {
				t.Fatalf("%s in %s: present=%v id=%d", row.Key, envs[i].Name, cell.Present, cell.SecretID)
			}
		}
	}
}

// A value that will not decrypt is not equal to anything — least of all to an
// empty box in the environment beside it.
func TestCompareWillNotCallAnUnreadableValueAMatch(t *testing.T) {
	envs := []Env{{ID: 1, Name: "staging"}, {ID: 2, Name: "production"}}
	got := Compare(envs, [][]Secret{
		{{ID: 11, Key: "TOKEN", Value: "", Unreadable: true}},
		{{ID: 21, Key: "TOKEN", Value: ""}},
	})
	if got.Rows[0].Cells[0].State != CompareUnreadable {
		t.Fatalf("cell state %s", got.Rows[0].Cells[0].State)
	}
	if got.Rows[0].State != CompareDifferent {
		t.Fatalf("row state %s", got.Rows[0].State)
	}
}
