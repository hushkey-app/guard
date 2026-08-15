// Package registries is the endpoint layer for container registries.
//
// Everything the dashboard shows — registries, repositories, tags — is read
// live from the provider with a stored account key, so nothing here can go
// stale and nothing but the key needs deleting. Every request the provider
// sees is made from the server; the browser never holds a credential.
//
// The key itself belongs to server/apis/cloud: it is one account key at the
// provider, and the cluster and storage pages open the same one. This package
// borrows the door to it and owns neither the key nor the client.
//
// Nothing here knows which provider it is talking to. An account resolves to
// whatever implements the registries half of the provider vocabulary, and an
// account at a provider with no registries is refused with that sentence
// rather than with an empty list that looks like an account with none.
package registries

import (
	"github.com/hushkey-app/guard/internal/cloud"
	apicloud "github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// open resolves one account to the registries half of its provider — the
// pair every call in this package starts with.
func open(accountID int64) (cloud.Registries, cloud.Credentials, error) {
	return apicloud.Registries(accountID)
}

// maker is open for the two calls that add and remove registries. A provider
// that has none to offer — Cloudflare's registry comes with the account and
// cannot be ordered or cancelled — is refused here, which is the same answer
// the dashboard gets from the capability before it draws the button.
func maker(accountID int64) (cloud.RegistryMaker, cloud.Credentials, error) {
	provider, creds, err := apicloud.Open(accountID)
	if err != nil {
		return nil, creds, err
	}
	made, ok := provider.(cloud.RegistryMaker)
	if !ok {
		return nil, creds, api.BadRequest(
			cloud.Unsupported(provider.Describe().Label, "create or delete registries").Error())
	}
	return made, creds, nil
}

// fail is the shared translation of a provider error into a status.
func fail(err error) error { return apicloud.Fail(err) }
