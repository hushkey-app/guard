package config

// The development `.env`.
//
// On a box, the environment comes from the unit file and guard's own settings come
// from its database. On a laptop there is no unit file, and the thing everybody
// already has in a checkout is a `.env` — so in development guard reads one at
// startup and writes it back when something is saved. `make dev` runs in the repo
// root, so the file lands beside the code, and every other tool that reads a
// `.env` — docker compose, a test script, direnv — sees the same values.
//
// Two rules, both the conventional ones:
//
//   - **A real environment variable wins over the file.** `GUARD_DB_PATH=x make
//     dev` has to mean what it says, and a file that overrode the shell would be a
//     file somebody debugs for an hour. Set-but-empty counts as unset, because
//     that is how every other reader in guard treats it.
//   - **Lines guard does not own are left alone.** The file is rewritten on save,
//     and anything that is not one of guard's variables — a comment, somebody's
//     `STRIPE_KEY`, a blank line — passes through untouched.
//
// It is off for a released build. A binary on a server that quietly wrote a
// `.env` into whatever directory systemd started it in would be a surprise, and
// the box has a better place for all of this.

import (
	"os"
	"strings"

	"github.com/hushkey-app/guard/internal/build"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// DefaultDotEnv is the file, relative to wherever guard was started.
const DefaultDotEnv = ".env"

// dotEnv is the path to read and write, or empty for "not in this deployment".
//
// `GUARD_DOTENV` names one explicitly — including on a release build, for somebody
// who wants it — and `GUARD_DOTENV=0` turns it off in development.
func dotEnv() string {
	if named, ok := os.LookupEnv("GUARD_DOTENV"); ok {
		if named == "" || named == "0" || strings.EqualFold(named, "false") {
			return ""
		}
		return named
	}
	if build.IsDevelopment(build.Tag()) {
		return DefaultDotEnv
	}
	return ""
}

// readDotEnv puts the file into the environment, without overriding what is
// already there. Called before the stored configuration is applied, because as far
// as everything downstream is concerned this file *is* the environment.
func readDotEnv() (string, []string) {
	path := dotEnv()
	if path == "" {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return path, nil
	}
	var read []string
	pairs, _ := model.ParseEnv(string(raw))
	for _, pair := range pairs {
		// Set-but-empty counts as unset, which is how everything else in guard
		// reads the environment: `env()` and its siblings all test for "".
		if os.Getenv(pair.Key) != "" {
			continue
		}
		if err := os.Setenv(pair.Key, pair.Value); err != nil {
			continue
		}
		read = append(read, pair.Key)
	}
	return path, read
}

// writeDotEnv mirrors the stored configuration into the file.
//
// Guard's variables are rewritten as one block; everything else in the file is
// kept exactly as it was, in the order it was, because a development `.env` is
// also where somebody's own variables live and losing those to a settings save
// would be worse than not having this at all.
func writeDotEnv(stored map[string]string) error {
	path := dotEnv()
	if path == "" {
		return nil
	}
	managed := map[string]bool{}
	for _, entry := range Entries {
		managed[entry.Name] = true
	}
	// Two passes over the existing file. Lines that are not guard's are kept
	// verbatim, in order. A guard line that guard has no stored value for is *also*
	// kept — it was typed into this file by hand, and this file is one of the two
	// places somebody is invited to edit; dropping it because the database has
	// nothing to say about it would make a settings save delete somebody's work.
	var keep []string
	byHand := map[string]string{}
	if raw, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
			trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
			if strings.HasPrefix(trimmed, "# guard —") {
				continue
			}
			name, _, isPair := strings.Cut(trimmed, "=")
			name = strings.TrimSpace(name)
			if isPair && managed[name] {
				if stored[name] == "" {
					byHand[name] = line
				}
				continue
			}
			keep = append(keep, line)
		}
	}
	for len(keep) > 0 && strings.TrimSpace(keep[len(keep)-1]) == "" {
		keep = keep[:len(keep)-1]
	}

	var out strings.Builder
	for _, line := range keep {
		out.WriteString(line + "\n")
	}
	if len(keep) > 0 {
		out.WriteString("\n")
	}
	out.WriteString("# guard — written from Settings → Configuration. Edit either; a save rewrites these lines.\n")
	for _, entry := range Entries {
		if entry.Bootstrap {
			continue
		}
		if value := stored[entry.Name]; value != "" {
			out.WriteString(entry.Name + "=" + model.EnvQuote(value) + "\n")
			continue
		}
		if line, ok := byHand[entry.Name]; ok {
			out.WriteString(strings.TrimRight(line, " \t") + "\n")
		}
	}
	if err := os.WriteFile(path, []byte(out.String()), 0o600); err != nil {
		return err
	}
	// WriteFile's mode only applies when it creates the file, and this one holds
	// the operator token in a directory somebody shares a screen of.
	return os.Chmod(path, 0o600)
}
