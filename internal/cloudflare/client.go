// Package cloudflare talks to one Cloudflare account on the dashboard's
// behalf: the R2 buckets it owns and the container registry that comes with
// it.
//
// The same rules as the other provider. Nothing is cached — a bucket's
// location and an image's tags are Cloudflare's state, and a copy of them in
// SQLite could only be wrong. Every request leaves from the server. The
// credentials the provider hands back, which here is a short-lived docker
// password for the managed registry, never leave this package.
//
// Two things are different enough to be worth naming.
//
// The token is not enough on its own. Every endpoint hangs off
// /accounts/{account_id}, so an account here is a token *and* an account id,
// which is why the stored account carries one and Vultr's does not. The id is
// not a secret — it is on the dashboard's overview page — and it is typed in
// rather than discovered, because a token that can see two accounts would
// otherwise have guard guessing which one somebody meant.
//
// The answers are wrapped. Cloudflare replies with {success, errors, result}
// and an HTTP 200 for several things that did not work, so the envelope is
// unwrapped in one place and the provider's own sentence is what comes out.
package cloudflare

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

// Client speaks to Cloudflare's account API and to the managed registry it
// hands out credentials for. The zero value is not usable; call New.
type Client struct {
	http *http.Client
	base string
	// registry is the managed registry's host. A field rather than a
	// constant so a test can stand one up; there is exactly one in
	// production and it is the same for every account.
	registry string
}

// ManagedRegistry is where Cloudflare keeps the images of every account. It
// is not created and it is not deleted: it arrives with the account, and an
// image in it is addressed by the account id it is pushed under.
const ManagedRegistry = "registry.cloudflare.com"

func New() *Client {
	return &Client{
		http:     &http.Client{Timeout: 20 * time.Second},
		base:     "https://api.cloudflare.com/client/v4",
		registry: ManagedRegistry,
	}
}

// NewFor points the client at a fake provider — the tests' door. Both halves
// answer on the same base, the same way the real ones are two hosts.
func NewFor(base, registry string, client *http.Client) *Client {
	return &Client{http: client, base: base, registry: registry}
}

// envelope is every account-API answer. Errors arrive inside a 200 often
// enough that the status alone cannot be trusted.
type envelope struct {
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	ResultInfo struct {
		Cursor  string `json:"cursor"`
		PerPage int    `json:"per_page"`
	} `json:"result_info"`
}

// bucketMissing is the code Cloudflare uses for a bucket that is not there.
// Named because it arrives inside a successful-looking body, and "that bucket
// is gone" deserves a 404 rather than a 400.
const bucketMissing = 10006

// refused are the codes for a token the provider would not accept. They
// arrive with an HTTP 400 rather than a 401, so the status cannot be what
// tells somebody they typed the token wrong.
var refused = map[int]bool{9106: true, 10000: true}

// call makes one account-API call and decodes result into out, which may be
// nil. The path is everything after /accounts/{id}.
func (c *Client) call(ctx context.Context, creds cloud.Credentials, method, path string, body, out any) error {
	_, err := c.do(ctx, creds, method, path, body, out)
	return err
}

// do is call plus the envelope, which the paged reads need for the cursor and
// nothing else does.
func (c *Client) do(ctx context.Context, creds cloud.Credentials, method, path string, body, out any) (envelope, error) {
	if strings.TrimSpace(creds.Account) == "" {
		return envelope{}, fmt.Errorf("this account has no Cloudflare account id stored")
	}
	address := c.base + "/accounts/" + url.PathEscape(creds.Account) + path
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return envelope{}, err
		}
		payload = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, address, payload)
	if err != nil {
		return envelope{}, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.Key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return envelope{}, fmt.Errorf("cloudflare did not answer: %w", err)
	}
	defer response.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return envelope{}, fmt.Errorf("cloudflare refused the api token (%s): %s", response.Status, reason(answer))
	}
	var wrapper envelope
	if err := json.Unmarshal(answer, &wrapper); err != nil {
		if response.StatusCode >= 300 {
			return envelope{}, fmt.Errorf("cloudflare answered %s: %s", response.Status, condense(answer))
		}
		return envelope{}, fmt.Errorf("cloudflare answered something unreadable: %w", err)
	}
	if !wrapper.Success {
		for _, failure := range wrapper.Errors {
			if failure.Code == bucketMissing {
				return wrapper, cloud.ErrNotFound
			}
			if refused[failure.Code] {
				return wrapper, fmt.Errorf("cloudflare refused the api token: %s", reason(answer))
			}
		}
		if response.StatusCode == http.StatusNotFound {
			return wrapper, cloud.ErrNotFound
		}
		return wrapper, fmt.Errorf("cloudflare answered: %s", reason(answer))
	}
	if out == nil || len(wrapper.Result) == 0 || string(wrapper.Result) == "null" {
		return wrapper, nil
	}
	if err := json.Unmarshal(wrapper.Result, out); err != nil {
		return wrapper, fmt.Errorf("cloudflare answered something unreadable: %w", err)
	}
	return wrapper, nil
}

// paged walks a cursor-paginated list, handing each answer's result to page.
// It stops on an empty cursor and on a cursor that repeats, because a list
// endpoint that keeps handing back the same page is a loop rather than a lot
// of data.
func (c *Client) paged(ctx context.Context, creds cloud.Credentials, path string, page func(json.RawMessage) error) error {
	seen := map[string]bool{}
	cursor := ""
	for {
		address := path
		if strings.Contains(address, "?") {
			address += "&per_page=100"
		} else {
			address += "?per_page=100"
		}
		if cursor != "" {
			address += "&cursor=" + url.QueryEscape(cursor)
		}
		var raw json.RawMessage
		wrapper, err := c.do(ctx, creds, http.MethodGet, address, nil, &raw)
		if err != nil {
			return err
		}
		if err := page(raw); err != nil {
			return err
		}
		next := wrapper.ResultInfo.Cursor
		if next == "" || seen[next] {
			return nil
		}
		seen[next] = true
		cursor = next
	}
}

// reason pulls the provider's own sentence out of an answer. Cloudflare
// nests a JSON string inside the message for some products, which reads as
// noise on a card, so the inner error is unwrapped when there is one.
func reason(body []byte) string {
	var wrapper envelope
	if err := json.Unmarshal(body, &wrapper); err != nil || len(wrapper.Errors) == 0 {
		return condense(body)
	}
	messages := make([]string, 0, len(wrapper.Errors))
	for _, failure := range wrapper.Errors {
		message := failure.Message
		var inner struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(message), &inner) == nil && inner.Error != "" {
			message = inner.Error
		}
		messages = append(messages, strings.TrimSpace(message))
	}
	return condense([]byte(strings.Join(messages, "; ")))
}

// condense flattens an error body to one short line for an error message.
func condense(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}
