// Container registries: the half of the account API that owns images, plus
// the registry's own docker token flow.
//
// Two APIs, one provider. Vultr's account API answers what registries a key
// owns, what repositories they hold, and hands back the docker credentials
// for each registry — so the account key stays the only secret guard stores.
// The registry itself (a Harbor behind *.vultrcr.com) answers what tags a
// repository holds, because the account API stops at the repository level.

package vultr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

// Registry is one container registry as the account API describes it. The
// docker credentials ride along unexported: Tags and DeleteTag need them,
// and keeping them unexportable is what makes "an endpoint accidentally
// returned the password" impossible to write.
type Registry struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	URN                 string    `json:"urn"`
	Region              string    `json:"region"`
	Public              bool      `json:"public"`
	Created             time.Time `json:"created"`
	StorageUsedBytes    int64     `json:"storage_used_bytes"`
	StorageAllowedBytes int64     `json:"storage_allowed_bytes"`

	baseURL  string
	user     string
	password string
}

// Repository is one image line in a registry: guard shows the counts the
// provider keeps and holds on to the opaque image token, because that —
// not the name — is what the delete endpoint addresses.
type Repository struct {
	Name          string `json:"name"`
	Image         string `json:"image"`
	Description   string `json:"description,omitempty"`
	AddedAt       string `json:"added_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	PullCount     int64  `json:"pull_count"`
	ArtifactCount int64  `json:"artifact_count"`
}

// Tag is one tag with what one manifest request could say about it. Size is
// the sum of the compressed layers, and zero for a multi-arch index — the
// index names other manifests rather than layers, and a guessed number
// would be worse than a dash.
type Tag struct {
	Name      string `json:"name"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"size_bytes"`
}

// vultrRegistry is the account API's shape, wider than what guard keeps.
type vultrRegistry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URN         string `json:"urn"`
	DateCreated string `json:"date_created"`
	Public      bool   `json:"public"`
	Storage     struct {
		Used    struct{ Bytes int64 `json:"bytes"` } `json:"used"`
		Allowed struct{ Bytes int64 `json:"bytes"` } `json:"allowed"`
	} `json:"storage"`
	RootUser struct {
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"root_user"`
	Metadata struct {
		Region struct {
			Name    string `json:"name"`
			BaseURL string `json:"base_url"`
		} `json:"region"`
	} `json:"metadata"`
}

// registry is the account API's row, narrowed to what guard keeps — plus the
// docker credentials, which stay unexported all the way to the token call.
func (v vultrRegistry) registry() Registry {
	created, _ := time.Parse("2006-01-02 15:04:05", v.DateCreated)
	return Registry{
		ID: v.ID, Name: v.Name, URN: v.URN, Region: v.Metadata.Region.Name,
		Public: v.Public, Created: created,
		StorageUsedBytes:    v.Storage.Used.Bytes,
		StorageAllowedBytes: v.Storage.Allowed.Bytes,
		baseURL:             v.Metadata.Region.BaseURL,
		user:                v.RootUser.Username,
		password:            v.RootUser.Password,
	}
}

// Registries lists every registry the key can see.
func (c *Client) Registries(ctx context.Context, apiKey string) ([]Registry, error) {
	var out []Registry
	cursor := ""
	for {
		address := c.base + "/v2/registries?per_page=300"
		if cursor != "" {
			address += "&cursor=" + url.QueryEscape(cursor)
		}
		var page struct {
			Registries []vultrRegistry `json:"registries"`
			Meta       struct {
				Links struct{ Next string `json:"next"` } `json:"links"`
			} `json:"meta"`
		}
		if err := c.vultr(ctx, apiKey, http.MethodGet, address, &page); err != nil {
			return nil, err
		}
		for _, r := range page.Registries {
			out = append(out, r.registry())
		}
		if page.Meta.Links.Next == "" {
			return out, nil
		}
		cursor = page.Meta.Links.Next
	}
}

// CreateRegistry orders one registry. It bills from the moment the provider
// accepts it, the same as an object storage subscription, which is why the
// endpoint above it says so in words rather than leaving it for the invoice.
//
// The plan is a name from RegistryPlans ("start_up"), the region a name from
// RegistryRegions ("sjc"). Both are the provider's own strings and neither is
// composed here.
func (c *Client) CreateRegistry(ctx context.Context, apiKey, name, region, plan string, public bool) (Registry, error) {
	body := map[string]any{"name": name, "region": region, "public": public}
	if plan != "" {
		body["plan"] = plan
	}
	var raw json.RawMessage
	if err := c.call(ctx, apiKey, http.MethodPost, c.base+"/v2/registry", body, &raw); err != nil {
		return Registry{}, err
	}
	return decodeRegistry(raw)
}

// DeleteRegistry cancels one registry — every repository, every tag, every
// artifact in it — and the subscription behind it.
func (c *Client) DeleteRegistry(ctx context.Context, apiKey, registryID string) error {
	return c.vultr(ctx, apiKey, http.MethodDelete, c.base+"/v2/registry/"+url.PathEscape(registryID), nil)
}

// RegistryRegion is one place a registry can be created.
type RegistryRegion struct {
	Name    string
	URN     string
	BaseURL string
	City    string
	Country string
}

// RegistryRegions lists where a registry may be created. Unlike the object
// storage clusters this needs no key of its own — it is the provider's price
// list — but it is read with one anyway, because everything else here is.
func (c *Client) RegistryRegions(ctx context.Context, apiKey string) ([]RegistryRegion, error) {
	var answer struct {
		Regions []struct {
			Name       string `json:"name"`
			URN        string `json:"urn"`
			BaseURL    string `json:"base_url"`
			DataCenter struct {
				City    string `json:"city"`
				Country string `json:"country"`
			} `json:"data_center"`
		} `json:"regions"`
	}
	if err := c.vultr(ctx, apiKey, http.MethodGet, c.base+"/v2/registry/region/list", &answer); err != nil {
		return nil, err
	}
	out := make([]RegistryRegion, 0, len(answer.Regions))
	for _, region := range answer.Regions {
		out = append(out, RegistryRegion{
			Name: region.Name, URN: region.URN, BaseURL: region.BaseURL,
			City: region.DataCenter.City, Country: region.DataCenter.Country,
		})
	}
	return out, nil
}

// RegistryPlan is one plan a registry can be created on.
type RegistryPlan struct {
	Name         string
	VanityName   string
	MaxStorageMB int
	MonthlyPrice float64
}

// RegistryPlans lists what a registry costs. The provider answers with an
// object keyed by plan name rather than a list, so the order is imposed here
// — cheapest first, which is the order somebody reads them in.
func (c *Client) RegistryPlans(ctx context.Context, apiKey string) ([]RegistryPlan, error) {
	var answer struct {
		Plans map[string]struct {
			VanityName   string  `json:"vanity_name"`
			MaxStorageMB int     `json:"max_storage_mb"`
			MonthlyPrice float64 `json:"monthly_price"`
		} `json:"plans"`
	}
	if err := c.vultr(ctx, apiKey, http.MethodGet, c.base+"/v2/registry/plan/list", &answer); err != nil {
		return nil, err
	}
	out := make([]RegistryPlan, 0, len(answer.Plans))
	for name, plan := range answer.Plans {
		out = append(out, RegistryPlan{
			Name: name, VanityName: plan.VanityName,
			MaxStorageMB: plan.MaxStorageMB, MonthlyPrice: plan.MonthlyPrice,
		})
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].MonthlyPrice < out[b].MonthlyPrice })
	return out, nil
}

// decodeRegistry reads a single-registry answer. The list endpoint wraps its
// rows in "registries" and the single-object endpoints have been seen both
// wrapped in "registry" and bare, so this accepts either rather than turning
// a successful create into "the provider answered something unreadable".
func decodeRegistry(raw []byte) (Registry, error) {
	var wrapped struct {
		Registry *vultrRegistry `json:"registry"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Registry != nil {
		return wrapped.Registry.registry(), nil
	}
	var bare vultrRegistry
	if err := json.Unmarshal(raw, &bare); err != nil {
		return Registry{}, fmt.Errorf("vultr answered something unreadable: %w", err)
	}
	return bare.registry(), nil
}

// Repositories lists what one registry holds.
func (c *Client) Repositories(ctx context.Context, apiKey, registryID string) ([]Repository, error) {
	var out []Repository
	cursor := ""
	for {
		address := c.base + "/v2/registry/" + url.PathEscape(registryID) + "/repositories?per_page=300"
		if cursor != "" {
			address += "&cursor=" + url.QueryEscape(cursor)
		}
		var page struct {
			Repositories []Repository `json:"repositories"`
			Meta         struct {
				Links struct{ Next string `json:"next"` } `json:"links"`
			} `json:"meta"`
		}
		if err := c.vultr(ctx, apiKey, http.MethodGet, address, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Repositories...)
		if page.Meta.Links.Next == "" {
			return out, nil
		}
		cursor = page.Meta.Links.Next
	}
}

// DeleteRepository removes one repository — every tag, every artifact —
// through the account API. The image token comes from Repositories; the
// name is only for people.
func (c *Client) DeleteRepository(ctx context.Context, apiKey, registryID, image string) error {
	address := c.base + "/v2/registry/" + url.PathEscape(registryID) + "/repository/" + url.PathEscape(image)
	return c.vultr(ctx, apiKey, http.MethodDelete, address, nil)
}

// tagWorkers bounds the manifest fan-out: one registry answering slowly
// should occupy a handful of connections, not one per tag.
const tagWorkers = 8

// Tags lists one repository's tags, each with the digest and size one
// manifest request can supply. The repo is the full name the registry knows
// ("hushkey/pack").
func (c *Client) Tags(ctx context.Context, reg Registry, repo string) ([]Tag, error) {
	token, err := c.token(ctx, reg, repo, "pull")
	if err != nil {
		return nil, err
	}
	var list struct {
		Tags []string `json:"tags"`
	}
	if err := c.harbor(ctx, reg, token, http.MethodGet, "/v2/"+repo+"/tags/list", nil, &list); err != nil {
		return nil, err
	}
	tags := make([]Tag, len(list.Tags))
	var wg sync.WaitGroup
	slots := make(chan struct{}, tagWorkers)
	for i, name := range list.Tags {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			tag := Tag{Name: name}
			// Best effort per tag: a manifest that cannot be read leaves a
			// bare name in the list rather than sinking the whole page.
			tag.Digest, tag.SizeBytes, _ = c.manifest(ctx, reg, token, repo, name)
			tags[i] = tag
		}(i, name)
	}
	wg.Wait()
	sort.SliceStable(tags, func(a, b int) bool { return tags[a].Name < tags[b].Name })
	return tags, nil
}

// DeleteTag removes the manifest behind one tag. That is the only delete
// the registry API has, and it means every tag sharing the digest goes with
// it — the endpoint's description says so, because the person deleting
// "v1.2-rc" deserves to know "v1.2" pointed at the same bytes.
func (c *Client) DeleteTag(ctx context.Context, reg Registry, repo, tag string) error {
	token, err := c.token(ctx, reg, repo, "pull,delete")
	if err != nil {
		return err
	}
	digest, _, err := c.manifest(ctx, reg, token, repo, tag)
	if err != nil {
		return err
	}
	if digest == "" {
		return fmt.Errorf("the registry did not name a digest for %s:%s", repo, tag)
	}
	return c.harbor(ctx, reg, token, http.MethodDelete, "/v2/"+repo+"/manifests/"+digest, nil, nil)
}

// manifestAccept is every manifest flavour a registry may hold, so the
// digest that comes back addresses what is actually stored rather than a
// converted-on-the-fly answer whose digest deletes nothing.
const manifestAccept = "application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json, " +
	"application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json"

func (c *Client) manifest(ctx context.Context, reg Registry, token, repo, tag string) (digest string, size int64, err error) {
	var body struct {
		Config struct{ Size int64 `json:"size"` } `json:"config"`
		Layers []struct{ Size int64 `json:"size"` } `json:"layers"`
	}
	digest, err = c.harborManifest(ctx, reg, token, repo, tag, &body)
	if err != nil {
		return "", 0, err
	}
	size = body.Config.Size
	for _, layer := range body.Layers {
		size += layer.Size
	}
	return digest, size, nil
}

// token runs the docker token flow: the registry's own credentials, from
// the account API, exchanged for a bearer scoped to one repository.
func (c *Client) token(ctx context.Context, reg Registry, repo, actions string) (string, error) {
	if reg.baseURL == "" || reg.user == "" {
		return "", fmt.Errorf("the account api did not include this registry's docker credentials")
	}
	address := reg.baseURL + "/service/token?service=harbor-registry&scope=" +
		url.QueryEscape("repository:"+repo+":"+actions)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(reg.user, reg.password)
	response, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("the registry did not answer: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return "", fmt.Errorf("the registry refused its own credentials (%s)", response.Status)
	}
	var answer struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil || answer.Token == "" {
		return "", fmt.Errorf("the registry's token answer was unreadable")
	}
	return answer.Token, nil
}

func (c *Client) harbor(ctx context.Context, reg Registry, token, method, path string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, reg.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("the registry did not answer: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode >= 300 {
		return fmt.Errorf("the registry answered %s: %s", response.Status, condense(body))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("the registry answered something unreadable: %w", err)
	}
	return nil
}

// harborManifest is harbor plus the one header that comes back: the digest
// arrives in Docker-Content-Digest, not in the body.
func (c *Client) harborManifest(ctx context.Context, reg Registry, token, repo, tag string, out any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reg.baseURL+"/v2/"+repo+"/manifests/"+url.PathEscape(tag), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", manifestAccept)
	response, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("the registry did not answer: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode >= 300 {
		return "", fmt.Errorf("the registry answered %s for %s:%s", response.Status, repo, tag)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return "", fmt.Errorf("manifest for %s:%s was unreadable: %w", repo, tag, err)
	}
	return response.Header.Get("Docker-Content-Digest"), nil
}
