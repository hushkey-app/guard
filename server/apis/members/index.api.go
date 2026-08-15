package members

import (
	"github.com/hushkey-app/guard/internal/auth"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/signin"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Roster is the members page: who may sign in, and the context needed to draw
// the page honestly.
//
// Enabled is part of the answer because the list is worth keeping either way. A
// fresh instance has no OAuth credentials yet, and being able to write the list
// before the keys arrive is the difference between "configure and it works" and
// "configure, then remember what you were going to do".
type Roster struct {
	// Enabled is false when no provider is configured — the list is stored and
	// enforced by nothing yet.
	Enabled bool `json:"enabled"`
	// Members is the environment's admins first, then the stored list.
	Members []model.Member `json:"members"`
	// You is the signed-in person, so the page can mark their own row and
	// refuse to offer them the button that locks them out. Empty when nobody
	// signed in, which is every request on an instance without sign-in.
	You model.Viewer `json:"you"`
}

var List = api.Define(api.Spec[api.None, api.None, Roster]{
	Name: "Members",
	// Admin: the members list is the guest list, and reading it tells you
	// exactly which addresses to phish.
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (Roster, error) {
		stored, err := store.Get().Members()
		if err != nil {
			return Roster{}, err
		}
		fixed := signin.Admins()
		// The environment's admins are not rows and cannot be, so they are
		// merged here rather than seeded into the table. A stored row for the
		// same address would be a duplicate that says something different
		// about the same person, so the fixed one wins and the other is
		// dropped from the answer.
		byEmail := make(map[string]bool, len(fixed))
		for _, admin := range fixed {
			byEmail[admin.Email] = true
		}
		list := make([]model.Member, 0, len(fixed)+len(stored))
		list = append(list, fixed...)
		for _, member := range stored {
			if byEmail[member.Email] {
				continue
			}
			list = append(list, member)
		}
		return Roster{Enabled: signin.Enabled(), Members: list, You: auth.Viewer(r.Context())}, nil
	},
})
