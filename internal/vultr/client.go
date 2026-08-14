// Package vultr talks to one Vultr account on the dashboard's behalf.
//
// One key, three surfaces. The account API answers what container registries
// the key owns, what compute instances it runs, and what object storage it
// has — so guard stores exactly one secret per account and reads everything
// else live. Nothing here is cached: a plan, a power state and a bucket's
// keys are the provider's state, and a copy of them in SQLite could only be
// wrong.
//
// Everything is dialled from the server, never the browser. The credentials
// the provider hands back — a registry's docker login, an object storage's
// S3 secret — live in unexported fields, so an endpoint returning one is a
// thing that has to be written on purpose rather than a thing that can
// happen by forgetting.
package vultr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/cloud"
)

// Client speaks to Vultr's account API and to the registries it names.
// The zero value is not usable; call New.
type Client struct {
	http *http.Client
	base string
}

func New() *Client {
	return &Client{
		// One bound for one upstream call. The tag listing fans out under a
		// caller context; this is the per-request ceiling underneath it.
		http: &http.Client{Timeout: 20 * time.Second},
		base: "https://api.vultr.com",
	}
}

// NewFor points the client at a fake provider — the tests' door.
func NewFor(base string, client *http.Client) *Client {
	return &Client{http: client, base: base}
}

// call makes one account-API call and decodes the answer into out, which may
// be nil for calls whose answer is only a status. body may be nil.
func (c *Client) call(ctx context.Context, apiKey, method, address string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, address, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("vultr did not answer: %w", err)
	}
	defer response.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return fmt.Errorf("vultr refused the api key (%s)", response.Status)
	case response.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case response.StatusCode >= 300:
		return fmt.Errorf("vultr answered %s: %s", response.Status, condense(answer))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return fmt.Errorf("vultr answered something unreadable: %w", err)
	}
	return nil
}

// vultr is call without a request body — the shape most reads take.
func (c *Client) vultr(ctx context.Context, apiKey, method, address string, out any) error {
	return c.call(ctx, apiKey, method, address, nil, out)
}

// ErrNotFound is the provider saying it has no such thing. Named because the
// endpoint layer turns it into a 404 and everything else into a 400: "that
// instance is gone" and "vultr is unhappy" are different sentences.
//
// It is the shared sentinel rather than one of this package's own, so that
// the endpoint layer can ask the question once for every provider.
var ErrNotFound = cloud.ErrNotFound

// paged walks a cursor-paginated list endpoint, calling each page's decoder.
// Vultr pages everything the same way, and a caller that forgets the cursor
// silently shows the first fifty of somebody's machines.
func (c *Client) paged(ctx context.Context, apiKey, path string, page func([]byte) (string, error)) error {
	cursor := ""
	for {
		address := c.base + path
		if strings.Contains(path, "?") {
			address += "&per_page=300"
		} else {
			address += "?per_page=300"
		}
		if cursor != "" {
			address += "&cursor=" + url.QueryEscape(cursor)
		}
		var raw json.RawMessage
		if err := c.vultr(ctx, apiKey, http.MethodGet, address, &raw); err != nil {
			return err
		}
		next, err := page(raw)
		if err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		cursor = next
	}
}

// nextCursor is the one field every paged answer carries.
type nextCursor struct {
	Meta struct {
		Links struct {
			Next string `json:"next"`
		} `json:"links"`
	} `json:"meta"`
}

// condense flattens an error body to one short line for an error message.
func condense(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}
