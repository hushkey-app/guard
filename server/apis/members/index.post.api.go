package members

import (
	"github.com/hushkey-app/guard/internal/auth"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/signin"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Invite is a form with two fields: an address, and whether it is an admin.
//
// There is no invitation email and no token, because there is nothing to send
// one to yet — the address is a claim about somebody who will prove it at
// Google or Apple. Adding it here means "when this address signs in, let it
// in", and until they do, the row is exactly that and nothing more.
type Invite struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (i Invite) Validate() error {
	return model.Member{Email: i.Email, Role: i.Role}.Validate()
}

// Add puts an address on the list, or changes the role of one already on it.
//
// Adding somebody who is already there as an admin is a promotion rather than
// an error: "make Ana an admin" and "add Ana as an admin" are the same
// intention, and making the second fail teaches people to remove somebody
// before they can promote them.
var Add = api.Define(api.Spec[api.None, Invite, model.Member]{
	Name:  "Add Member",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Invite]) (model.Member, error) {
		email := model.NormalizeEmail(r.Body.Email)
		// An address from GUARD_ADMIN_EMAIL is already an admin, by a rule this
		// table cannot express and cannot override. Storing a row for it would
		// only be a row that looks removable and is not.
		if signin.Fixed(email) {
			return model.Member{}, api.BadRequest("that address is an admin through GUARD_ADMIN_EMAIL already")
		}
		who := ""
		if viewer := auth.Viewer(r.Context()); viewer.SignedIn() {
			who = viewer.Email
		}
		member, err := store.Get().SaveMember(model.Member{Email: email, Role: r.Body.Role, AddedBy: who})
		if err != nil {
			return model.Member{}, api.BadRequest(err.Error())
		}
		return member, nil
	},
})
