// Package templates is the compose files guard keeps, and their revisions.
//
// Guard is the source of truth for the file, not the box. That is a deliberate
// reversal of how the machines' environments work, and it buys one thing: a
// template travels in the backup, so a replacement machine is provisioned by
// deploying to it rather than by somebody remembering what was in /srv.
//
// A save never overwrites. It writes the next version, because the question a
// deploy record has to answer months later is "what did we actually deploy",
// and a template edited in place answers it with today's file.
package templates

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/mirairoad/howl-go/core/api"
)

// Template is what a save sends. Omitting ID makes a new one; giving it writes
// the next version of that one.
type Template struct {
	ID          int64  `json:"id,omitempty"`
	Name        string `json:"name"`
	ServiceName string `json:"service_name"`
	Image       string `json:"image"`
	Path        string `json:"path"`
	ComposeYAML string `json:"compose_yaml"`
	HealthPath  string `json:"health_path,omitempty"`
	HealthPort  int    `json:"health_port,omitempty"`
	// SecretEnvID is the vault environment the vault-sourced variables are read
	// from, at deploy time. One environment per template: a staging template
	// cannot resolve a production value, which is the same rule a vault key
	// keeps.
	SecretEnvID int64               `json:"secret_env_id,omitempty"`
	Vars        []model.TemplateVar `json:"vars"`
}

// Validate names the field in the sentence, not only in the Field on the error.
// A dialog with eight boxes that answers "is required" has told somebody
// nothing, and this is the first thing anybody using this feature will read.
func (t Template) Validate() error {
	if t.Name == "" {
		return api.Invalid("name", "give the template a name")
	}
	// The service name, the image and the directory are not asked for: they are
	// derived from the name and from the compose file in Normalise, which is
	// also where a compose file that never mentions ${TAG} is refused. All
	// three are still accepted when sent, for a template that has to live
	// somewhere specific.
	if t.ComposeYAML == "" {
		return api.Invalid("compose_yaml", "the compose file is empty")
	}
	return nil
}

// Query picks a version on a read. Zero is the newest.
type Query struct {
	Version int `query:"version"`
}
