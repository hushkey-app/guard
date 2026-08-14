// Package s3 is the object half of object storage: the protocol the control
// APIs do not speak.
//
// Everything else guard asks a provider goes through that provider's own API —
// what buckets exist, what a plan costs, whether a machine is running. What is
// *inside* a bucket is not there. Objects are S3, signed with SigV4 against the
// storage's own hostname, and it is the same protocol at every provider, which
// is why this is one package rather than a method on each of them.
//
// It is deliberately three verbs: list the buckets, list one prefix, and sign a
// link to one object. No upload, no delete, no copy. Guard is a window onto
// somebody's data here, not a way through it, and a read-only client cannot
// destroy an object no matter what a session that reaches it is used for.
//
// The signing is written out rather than pulled in. It is one page of HMAC and
// two canonical strings, against which an SDK is a large dependency for a small
// job — and the small job is easier to read for the person who has to trust it.
package s3

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Keys is one S3 credential pair. It is the data-plane credential — it reads
// the objects themselves — which is why nothing here logs it and why the
// caller that holds it is the one endpoint layer that must.
type Keys struct {
	Access string
	Secret string
}

// Config is where one storage answers and what to sign for.
//
// Region is part of the signature rather than the address: R2 wants "auto",
// Ceph-backed providers ignore what is sent and take the region from the
// hostname. Getting it wrong is a refused signature, not a wrong bucket.
type Config struct {
	Endpoint string
	Region   string
}

type Client struct{ http *http.Client }

func New() *Client {
	return &Client{http: &http.Client{Timeout: 30 * time.Second}}
}

// NewFor is the tests' door.
func NewFor(client *http.Client) *Client { return &Client{http: client} }

// Bucket is one bucket on an endpoint, for the providers whose storage holds
// several. R2 has one bucket per storage and never needs this.
type Bucket struct {
	Name    string
	Created time.Time
}

// Object is one stored object.
type Object struct {
	Key      string
	Size     int64
	Modified time.Time
	ETag     string
	Class    string
}

// Listing is one page of one prefix: the folders directly under it, the
// objects directly in it, and where to carry on.
type Listing struct {
	Folders []string
	Objects []Object
	Cursor  string
}

// Buckets lists what the endpoint holds.
func (c *Client) Buckets(ctx context.Context, cfg Config, keys Keys) ([]Bucket, error) {
	body, err := c.get(ctx, cfg, keys, "/", nil)
	if err != nil {
		return nil, err
	}
	var answer struct {
		Buckets struct {
			Bucket []struct {
				Name         string `xml:"Name"`
				CreationDate string `xml:"CreationDate"`
			} `xml:"Bucket"`
		} `xml:"Buckets"`
	}
	if err := xml.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("the storage answered something unreadable: %w", err)
	}
	out := make([]Bucket, 0, len(answer.Buckets.Bucket))
	for _, bucket := range answer.Buckets.Bucket {
		created, _ := time.Parse(time.RFC3339, bucket.CreationDate)
		out = append(out, Bucket{Name: bucket.Name, Created: created})
	}
	sort.SliceStable(out, func(a, b int) bool {
		return strings.ToLower(out[a].Name) < strings.ToLower(out[b].Name)
	})
	return out, nil
}

// pageSize is one screenful and then some. The listing is a page in a table
// somebody is reading, not a backup tool.
const pageSize = 200

// List reads one prefix as a folder: the delimiter is what turns a flat
// keyspace into something with directories in it, and the provider does that
// work rather than guard downloading every key to group them.
func (c *Client) List(ctx context.Context, cfg Config, keys Keys, bucket, prefix, cursor string) (Listing, error) {
	query := url.Values{}
	query.Set("list-type", "2")
	query.Set("delimiter", "/")
	query.Set("max-keys", strconv.Itoa(pageSize))
	if prefix != "" {
		query.Set("prefix", prefix)
	}
	if cursor != "" {
		query.Set("continuation-token", cursor)
	}
	body, err := c.get(ctx, cfg, keys, "/"+bucket, query)
	if err != nil {
		return Listing{}, err
	}
	var answer struct {
		Contents []struct {
			Key          string `xml:"Key"`
			LastModified string `xml:"LastModified"`
			ETag         string `xml:"ETag"`
			Size         int64  `xml:"Size"`
			StorageClass string `xml:"StorageClass"`
		} `xml:"Contents"`
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
		NextContinuationToken string `xml:"NextContinuationToken"`
		IsTruncated           bool   `xml:"IsTruncated"`
	}
	if err := xml.Unmarshal(body, &answer); err != nil {
		return Listing{}, fmt.Errorf("the storage answered something unreadable: %w", err)
	}
	listing := Listing{Folders: []string{}, Objects: []Object{}}
	for _, folder := range answer.CommonPrefixes {
		listing.Folders = append(listing.Folders, folder.Prefix)
	}
	for _, object := range answer.Contents {
		// A key that is the prefix itself is the folder marker some tools
		// write. It is not a file anybody wants a row for.
		if object.Key == prefix {
			continue
		}
		modified, _ := time.Parse(time.RFC3339, object.LastModified)
		listing.Objects = append(listing.Objects, Object{
			Key: object.Key, Size: object.Size, Modified: modified,
			ETag: strings.Trim(object.ETag, `"`), Class: object.StorageClass,
		})
	}
	if answer.IsTruncated {
		listing.Cursor = answer.NextContinuationToken
	}
	return listing, nil
}

// Link signs a URL that fetches one object, and expires.
//
// The link is the download: it goes straight from the browser to the storage,
// so guard never carries the bytes, and it stops working on its own. Signing
// one is a read of somebody's data by whoever holds the link until it does, so
// the endpoint above this is admin and says so in the log.
func (c *Client) Link(cfg Config, keys Keys, bucket, key string, ttl time.Duration, now time.Time) (string, error) {
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return "", err
	}
	stamp := now.UTC()
	scope := fmt.Sprintf("%s/%s/s3/aws4_request", stamp.Format("20060102"), cfg.Region)
	query := url.Values{}
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-Credential", keys.Access+"/"+scope)
	query.Set("X-Amz-Date", stamp.Format("20060102T150405Z"))
	query.Set("X-Amz-Expires", strconv.Itoa(int(ttl.Seconds())))
	query.Set("X-Amz-SignedHeaders", "host")

	path := "/" + bucket + "/" + key
	canonical := strings.Join([]string{
		http.MethodGet,
		escapePath(path),
		canonicalQuery(query),
		"host:" + endpoint.Host + "\n",
		"host",
		// A presigned URL cannot carry a body hash, and every implementation
		// agrees on this literal instead.
		"UNSIGNED-PAYLOAD",
	}, "\n")
	signature := sign(keys.Secret, stamp, cfg.Region, stringToSign(stamp, scope, canonical))
	query.Set("X-Amz-Signature", signature)
	return endpoint.Scheme + "://" + endpoint.Host + escapePath(path) + "?" + canonicalQuery(query), nil
}

// get makes one signed GET and returns the body.
func (c *Client) get(ctx context.Context, cfg Config, keys Keys, path string, query url.Values) ([]byte, error) {
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	if keys.Access == "" || keys.Secret == "" {
		return nil, fmt.Errorf("this storage has no S3 credentials stored")
	}
	address := endpoint.Scheme + "://" + endpoint.Host + escapePath(path)
	if len(query) > 0 {
		address += "?" + canonicalQuery(query)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	stamp := time.Now().UTC()
	// The empty body's hash, spelled out: every signed request needs one and
	// none of the three reads here has a body.
	const emptyPayload = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	req.Header.Set("X-Amz-Date", stamp.Format("20060102T150405Z"))
	req.Header.Set("X-Amz-Content-Sha256", emptyPayload)
	req.Host = endpoint.Host

	scope := fmt.Sprintf("%s/%s/s3/aws4_request", stamp.Format("20060102"), cfg.Region)
	canonical := strings.Join([]string{
		http.MethodGet,
		escapePath(path),
		canonicalQuery(query),
		"host:" + endpoint.Host + "\n" +
			"x-amz-content-sha256:" + emptyPayload + "\n" +
			"x-amz-date:" + req.Header.Get("X-Amz-Date") + "\n",
		"host;x-amz-content-sha256;x-amz-date",
		emptyPayload,
	}, "\n")
	signature := sign(keys.Secret, stamp, cfg.Region, stringToSign(stamp, scope, canonical))
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=%s",
		keys.Access, scope, signature))

	response, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the storage did not answer: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if response.StatusCode >= 300 {
		return nil, fmt.Errorf("the storage answered %s: %s", response.Status, message(body))
	}
	return body, nil
}

// message pulls S3's own sentence out of an error document, which is XML and
// says more than the status does — "SignatureDoesNotMatch" is the difference
// between a wrong secret and a wrong region.
func message(body []byte) string {
	var answer struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if err := xml.Unmarshal(body, &answer); err == nil && answer.Code != "" {
		if answer.Message != "" {
			return answer.Code + " — " + answer.Message
		}
		return answer.Code
	}
	text := strings.Join(strings.Fields(string(body)), " ")
	if len(text) > 160 {
		text = text[:160] + "…"
	}
	return text
}

func stringToSign(stamp time.Time, scope, canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return strings.Join([]string{
		"AWS4-HMAC-SHA256",
		stamp.Format("20060102T150405Z"),
		scope,
		hex.EncodeToString(sum[:]),
	}, "\n")
}

func sign(secret string, stamp time.Time, region, payload string) string {
	key := hmacSum([]byte("AWS4"+secret), stamp.Format("20060102"))
	key = hmacSum(key, region)
	key = hmacSum(key, "s3")
	key = hmacSum(key, "aws4_request")
	return hex.EncodeToString(hmacSum(key, payload))
}

func hmacSum(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(value))
	return mac.Sum(nil)
}

// canonicalQuery is the query string sorted and escaped the way the signature
// expects, which is not the way url.Values.Encode does it in every case — the
// space in a key must be %20 rather than +, or the signature is over a
// different string than the one sent.
func canonicalQuery(query url.Values) string {
	if len(query) == 0 {
		return ""
	}
	names := make([]string, 0, len(query))
	for name := range query {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	for _, name := range names {
		for _, value := range query[name] {
			pairs = append(pairs, escape(name)+"="+escape(value))
		}
	}
	return strings.Join(pairs, "&")
}

// escapePath escapes a key without touching the separators, because the path
// is a path: "a b/c.txt" signs and fetches as "/a%20b/c.txt".
func escapePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = escape(part)
	}
	return strings.Join(parts, "/")
}

// escape is RFC 3986 unreserved-only escaping: url.QueryEscape turns a space
// into "+" and leaves "+" alone, and both are wrong here.
func escape(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9',
			b == '-', b == '_', b == '.', b == '~':
			out.WriteByte(b)
		default:
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}
