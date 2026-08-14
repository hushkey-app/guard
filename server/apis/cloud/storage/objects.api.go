package storage

import (
	"errors"
	"strings"

	"github.com/hushkey-app/guard/internal/cloud"
	apicloud "github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// ObjectQuery addresses one place inside one storage. The prefix is a folder
// and the cursor is where the last page stopped; both are the provider's own
// strings, handed back untouched.
type ObjectQuery struct {
	Account   int64  `query:"account"`
	Storage   string `query:"storage"`
	Container string `query:"container"`
	Prefix    string `query:"prefix"`
	Cursor    string `query:"cursor"`
}

func (q ObjectQuery) Validate() error {
	if q.Account <= 0 {
		return errors.New("account must name a stored cloud account")
	}
	if q.Storage == "" {
		return errors.New("storage is required")
	}
	if len(q.Prefix) > 1024 {
		return errors.New("that is not a prefix")
	}
	return nil
}

// Contents is one folder: the buckets a storage holds, or one page of one
// prefix inside one of them.
//
// Containers is filled only at the top of a storage that has any — a Vultr
// subscription holds buckets, an R2 bucket is one — and it is what tells the
// dashboard whether it is looking at a list of buckets or a list of files.
type Contents struct {
	Containers []cloud.Container `json:"containers"`
	Folders    []string          `json:"folders"`
	Objects    []cloud.Object    `json:"objects"`
	Cursor     string            `json:"cursor,omitempty"`
}

// Objects lists what is inside a storage, one folder at a time.
//
// This is the only read in guard that goes to the storage itself rather than
// to the provider's API — the account API stops at the bucket — so it is
// signed with an S3 credential, on the server, and the browser sees names and
// sizes. It is admin because a listing of somebody's object keys is data about
// their data: file names alone say plenty.
//
// The delimiter is what makes it a folder view rather than a dump of every
// key, and the provider does that grouping. A bucket with a million objects
// costs one page here, not a million rows.
var Objects = api.Define(api.Spec[ObjectQuery, api.None, Contents]{
	Name:  "Storage Objects",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[ObjectQuery, api.None]) (Contents, error) {
		browser, creds, err := apicloud.Browser(r.Query.Account)
		if err != nil {
			return Contents{}, err
		}
		out := Contents{Containers: []cloud.Container{}, Folders: []string{}, Objects: []cloud.Object{}}
		// At the top of a storage, ask what buckets it holds. An empty answer
		// means the storage is itself the bucket, and the listing carries on
		// into it rather than showing an empty level nobody can open.
		if r.Query.Container == "" {
			containers, err := browser.Containers(r.Context(), creds, r.Query.Storage)
			if err != nil {
				return Contents{}, fail(err)
			}
			if len(containers) > 0 {
				out.Containers = containers
				return out, nil
			}
		}
		listing, err := browser.Objects(r.Context(), creds, cloud.ObjectRef{
			Storage:   r.Query.Storage,
			Container: r.Query.Container,
			Prefix:    strings.TrimPrefix(r.Query.Prefix, "/"),
			Cursor:    r.Query.Cursor,
		})
		if err != nil {
			return Contents{}, fail(err)
		}
		if listing.Folders != nil {
			out.Folders = listing.Folders
		}
		if listing.Objects != nil {
			out.Objects = listing.Objects
		}
		out.Cursor = listing.Cursor
		return out, nil
	},
})
