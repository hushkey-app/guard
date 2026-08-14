package members

import (
	"database/sql"
	"errors"
	"log/slog"

	"github.com/hushkey-app/guard/internal/auth"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/signin"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Removed says what a removal actually did, because it does two things.
type Removed struct {
	Email string `json:"email"`
	// Sessions is how many open browsers were signed out. Taking somebody off
	// the list has to reach the tab they left open, or the removal is a note
	// to self rather than a revocation.
	Sessions int64 `json:"sessions"`
}

// Remove takes an address off the list and ends every session it holds.
//
// Two things it will not do. It will not remove an address that comes from
// GUARD_ADMIN_EMAIL, because that one is not a row and deleting the row would
// change nothing while looking like it had. And it will not remove the person
// asking: locking yourself out of a dashboard by pressing a button next to your
// own name is a mistake that takes an environment variable and a restart to
// undo.
var Remove = api.Define(api.Spec[api.None, api.None, Removed]{
	Name:  "Remove Member",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (Removed, error) {
		email := model.NormalizeEmail(r.Param("email"))
		if email == "" {
			return Removed{}, api.Invalid("email", "is required")
		}
		if signin.Fixed(email) {
			return Removed{}, api.BadRequest("that address is an admin through GUARD_ADMIN_EMAIL — unset it and restart to remove them")
		}
		if viewer := auth.Viewer(r.Context()); viewer.SignedIn() && model.NormalizeEmail(viewer.Email) == email {
			return Removed{}, api.BadRequest("you cannot remove yourself — ask another admin")
		}
		if err := store.Get().RemoveMember(email); errors.Is(err, sql.ErrNoRows) {
			return Removed{}, api.NotFound("that address is not a member")
		} else if err != nil {
			return Removed{}, err
		}
		sessions, err := store.Get().DeleteSessionsFor(email)
		if err != nil {
			return Removed{}, err
		}
		slog.Info("member removed", slog.String("email", email), slog.Int64("sessions", sessions))
		return Removed{Email: email, Sessions: sessions}, nil
	},
})
