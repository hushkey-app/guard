package model

import (
	"strings"
	"testing"
)

const packCompose = `services:
  app:
    image: syd.vultrcr.com/hushkey/pack:${TAG}
    env_file: .env
    ports:
      - "8000:8000"
  caddy:
    image: caddy:2-alpine
    ports:
      - "80:80"

volumes:
  caddy_data:
`

func TestTheServiceIsReadFromTheComposeFileNotTheTemplateName(t *testing.T) {
	if got := ServicesInCompose(packCompose); strings.Join(got, ",") != "app,caddy" {
		t.Fatalf("services are %v", got)
	}
	// The one carrying ${TAG} is the one a deploy is about — caddy is pinned
	// and is nobody's deploy.
	if got := ServiceForTag(packCompose); got != "app" {
		t.Fatalf("the tagged service is %q", got)
	}
	// And that is what a template called something else ends up with.
	template := DeployTemplate{Name: "PACK-APP", ComposeYAML: packCompose, HealthPath: "/health"}
	if err := template.Normalise(); err != nil {
		t.Fatal(err)
	}
	if template.ServiceName != "app" {
		t.Fatalf("normalised to %q, want app — the slug of the template name is not the service", template.ServiceName)
	}
	if err := template.Validate(); err != nil {
		t.Fatal(err)
	}
}

// The failure this replaces is `docker exited 1: no such service: pack-app`,
// found on the machine at deploy time rather than in the dialog at save time.
func TestAServiceTheComposeFileDoesNotHaveIsRefused(t *testing.T) {
	template := DeployTemplate{
		Name: "pack-app", ServiceName: "pack-app", Image: "r/app",
		Path: "/guard/pack-app", ComposeYAML: packCompose,
	}
	err := template.Validate()
	if err == nil {
		t.Fatal("a service that is not in the compose file was accepted")
	}
	for _, want := range []string{"pack-app", "app", "caddy"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// A file that declares no services at all is somebody's problem elsewhere —
// ImageInCompose already refuses it for having no ${TAG}. This check must not
// be what fails, or the message would be about the wrong thing.
func TestAComposeFileWithNoServicesIsNotThisCheckSProblem(t *testing.T) {
	if got := ServicesInCompose("services: {}\n"); len(got) != 0 {
		t.Fatalf("found %v", got)
	}
	if got := ServiceForTag("services: {}\n"); got != "" {
		t.Fatalf("found %q", got)
	}
}

// Comments, blank lines and a services block that is not first.
func TestTheServiceScanSurvivesAnOrdinaryFile(t *testing.T) {
	compose := `# pack, production
name: pack

volumes:
  data:

services:

  # the application
  app:
    image: r/app:${TAG}

  worker:
    image: r/app:${TAG}
`
	if got := ServicesInCompose(compose); strings.Join(got, ",") != "app,worker" {
		t.Fatalf("services are %v", got)
	}
	if got := ServiceForTag(compose); got != "app" {
		t.Fatalf("the tagged service is %q", got)
	}
}
