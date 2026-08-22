package model

import (
	"strings"
	"testing"
)

// The failure this prevents is silent and expensive: the container starts, it
// has none of the variables, it falls back to its own defaults, and it answers
// the health check while talking to the wrong database — or to no database, and
// the gate passes anyway because something is listening.
func TestAVariableThatReachesNothingIsRefused(t *testing.T) {
	base := "services:\n  app:\n    image: r/app:${TAG}\n    ports: [\"80:8000\"]\n"
	template := DeployTemplate{
		Name: "pack", ServiceName: "app", Image: "r/app", Path: "/guard/pack",
		ComposeYAML: base,
		Vars:        []TemplateVar{{Key: "DATABASE_URL", Source: VarStatic, Value: "postgres://…"}},
	}
	err := template.Validate()
	if err == nil {
		t.Fatal("a variable the compose file never delivers was accepted")
	}
	for _, want := range []string{"DATABASE_URL", "env_file", "app"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// Three ways an author can have thought about delivery, and all of them pass.
// The check is generous on purpose: refusing a template that works would be
// worse than the silence it replaces.
func TestTheWaysAVariableIsActuallyDeliveredAllPass(t *testing.T) {
	for _, shape := range []struct{ what, compose string }{
		{"env_file", "services:\n  app:\n    image: r/app:${TAG}\n    env_file: .env\n"},
		{"env_file list", "services:\n  app:\n    image: r/app:${TAG}\n    env_file:\n      - .env\n"},
		{"interpolated", "services:\n  app:\n    image: r/app:${TAG}\n    environment:\n      DB: ${DATABASE_URL}\n"},
		{"pass-through", "services:\n  app:\n    image: r/app:${TAG}\n    environment:\n      - DATABASE_URL\n"},
	} {
		template := DeployTemplate{
			Name: "pack", ServiceName: "app", Image: "r/app", Path: "/guard/pack",
			ComposeYAML: shape.compose,
			Vars:        []TemplateVar{{Key: "DATABASE_URL", Source: VarStatic, Value: "postgres://…"}},
		}
		if err := template.Validate(); err != nil {
			t.Fatalf("%s was refused: %v", shape.what, err)
		}
	}
}

// A template with no variables at all is the ordinary case and says nothing.
func TestATemplateWithNoVariablesIsNotAskedAboutDelivery(t *testing.T) {
	template := DeployTemplate{
		Name: "pack", ServiceName: "app", Image: "r/app", Path: "/guard/pack",
		ComposeYAML: "services:\n  app:\n    image: r/app:${TAG}\n",
	}
	if err := template.Validate(); err != nil {
		t.Fatalf("refused: %v", err)
	}
}

// Every undelivered name is listed, not just the first: somebody fixing this
// should have to open the dialog once.
func TestEveryUndeliveredVariableIsNamed(t *testing.T) {
	missing := UndeliveredVars("services:\n  app:\n    image: r/app:${TAG}\n", []TemplateVar{
		{Key: "DATABASE_URL"}, {Key: "REDIS_URL"}, {Key: "TAG"},
	})
	if len(missing) != 2 || missing[0] != "DATABASE_URL" || missing[1] != "REDIS_URL" {
		t.Fatalf("named %v", missing)
	}
}
