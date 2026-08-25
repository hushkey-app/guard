package pages

import "github.com/hushkey-app/guard/internal/telemetry/model"

// Bootstrap is request-time state shared by SSR and the browser renderer.
// Values here must be safe to send to the browser; secrets never belong in it.
type Bootstrap struct {
	Viewer  model.Viewer `json:"viewer"`
	Version string       `json:"version"`
}
