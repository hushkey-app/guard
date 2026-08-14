package model

import (
	"errors"
	"strings"
)

// A ProviderAccount is one stored way into a cloud account — today always a
// Vultr account API key. The key is what unlocks everything else: with it the
// provider's API lists the container registries it owns and returns the
// docker credentials for each, names the compute instances it runs, and
// answers for the object storage attached to it.
//
// One account, three surfaces, because that is what the key actually is. It
// began as a registry credential and it is the same secret either way —
// making the cluster page ask for it a second time would only mean the same
// string stored twice and revoked once.
//
// This is deliberately not a registry, an instance or a bucket. All of those
// are live state read from the provider on demand; storing them would only
// let them go stale.
type ProviderAccount struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Provider names the API the key speaks to, and with it what the account
	// can be asked for: Vultr answers for registries, machines and object
	// storage; Cloudflare answers for R2 and, for now, nothing else. What
	// each one can do is derived from what its provider implements, in
	// internal/cloud — never spelled out again here.
	Provider string `json:"provider"`
	// ExternalID is the provider's own id for the account, for the providers
	// whose every endpoint hangs off one. Cloudflare needs it; Vultr's key
	// names its own account and leaves this empty.
	//
	// It is not a secret and it is not stored sealed: it is on the account's
	// overview page, and it is typed in rather than discovered because a
	// token that can see two accounts would otherwise have guard guessing
	// which one was meant.
	ExternalID string `json:"external_id,omitempty"`
	// APIKey is write-only. A pointer for the same reason the SSH password
	// is one: absent means "leave it", and the read side never carries it.
	APIKey *string `json:"api_key,omitempty"`
	// HasKey is the read side. The key itself never leaves the server — the
	// dashboard is told one exists, and draws dots.
	HasKey bool `json:"has_key,omitempty"`
	// S3Access and S3Secret are the *data-plane* pair, for the providers whose
	// objects can only be reached over S3 with a credential their account key
	// cannot mint. Optional: an account without one lists buckets, makes them
	// and deletes them, and cannot look inside.
	//
	// The access key is an id and is read back; the secret is write-only and
	// sealed beside the API key, the same contract as the SSH passwords. They
	// are a second secret rather than a second field on the first one because
	// they can do a different thing: this pair reads somebody's data.
	S3Access string  `json:"s3_access_key,omitempty"`
	S3Secret *string `json:"s3_secret_key,omitempty"`
	HasS3    bool    `json:"has_s3_keys,omitempty"`
}

func (a ProviderAccount) Validate() error {
	if strings.TrimSpace(a.Name) == "" {
		return errors.New("name is required")
	}
	if len(a.Name) > 80 {
		return errors.New("name must be 80 characters or fewer")
	}
	if a.Provider != "" && !KnownProvider(a.Provider) {
		return errors.New("that is not a provider guard knows how to talk to")
	}
	if len(a.ExternalID) > 128 {
		return errors.New("that is not an account id")
	}
	if len(a.S3Access) > 256 {
		return errors.New("that is not an access key")
	}
	// Half a pair signs nothing, and storing half of one would mean a browse
	// button that fails at the signature rather than at the form.
	if (a.S3Access != "") != (a.S3Secret != nil && strings.TrimSpace(*a.S3Secret) != "") {
		return errors.New("an S3 access key and its secret are stored together or not at all")
	}
	return nil
}

// The providers guard knows. Named rather than spelled at each use, so that
// adding one is a line here and a package beside internal/vultr — and so the
// compiler helps at every place that switches on them.
//
// Whether a given provider can do a given thing is not written down here. It
// is derived in internal/cloud from what the provider actually implements,
// because a capability a model could claim and a package could lack is a
// button that fails.
const (
	ProviderVultr      = "vultr"
	ProviderCloudflare = "cloudflare"
)

// Providers is every provider id, in the order the account form offers them.
var Providers = []string{ProviderVultr, ProviderCloudflare}

// KnownProvider says whether an id names one of them.
func KnownProvider(id string) bool {
	for _, known := range Providers {
		if id == known {
			return true
		}
	}
	return false
}

// A ProviderLink ties one declared machine to one instance in one account.
//
// Two ids and nothing else, because everything worth knowing about the
// instance — its power state, its plan, its address, its bandwidth — is the
// provider's to answer and goes stale the moment it is copied. What guard
// stores is the association it made, which nothing else can answer.
//
// The instance id rather than the address: a machine keeps its id across a
// reinstall and loses its IP to any number of ordinary events, and a link
// that silently re-pointed at whoever holds the address now would be worse
// than no link at all.
type ProviderLink struct {
	NodeID     int64  `json:"node_id"`
	AccountID  int64  `json:"account_id"`
	Provider   string `json:"provider"`
	InstanceID string `json:"instance_id"`
}

func (l ProviderLink) Validate() error {
	if l.AccountID <= 0 {
		return errors.New("a cloud account is required")
	}
	if strings.TrimSpace(l.InstanceID) == "" {
		return errors.New("an instance is required")
	}
	if len(l.InstanceID) > 128 {
		return errors.New("that is not an instance id")
	}
	// A link is to a machine, and machines are Vultr's half of the world:
	// Cloudflare has no instance to link one to, so an account there cannot
	// be on the other end of this.
	if l.Provider != "" && l.Provider != ProviderVultr {
		return errors.New("only a Vultr account has machines to link to")
	}
	return nil
}
