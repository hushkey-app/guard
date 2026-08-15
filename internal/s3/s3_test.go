package s3

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

var keys = Keys{Access: "AKIAEXAMPLE", Secret: "secret"}

func fake(t *testing.T, body string) (Config, *Client) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return Config{Endpoint: server.URL, Region: "auto"}, NewFor(server.Client())
}

// The listing is a folder view: the delimiter is what makes one, and the key
// that is the prefix itself is a marker rather than a file.
func TestListReadsAPrefixAsAFolder(t *testing.T) {
	const answer = `<?xml version="1.0"?><ListBucketResult>
	  <CommonPrefixes><Prefix>user/</Prefix></CommonPrefixes>
	  <Contents><Key>photos/</Key><Size>0</Size><LastModified>2026-07-15T17:33:25Z</LastModified></Contents>
	  <Contents><Key>photos/avatar.webp</Key><Size>13046</Size><ETag>&quot;abc&quot;</ETag>
	    <LastModified>2026-07-15T17:33:25Z</LastModified><StorageClass>STANDARD</StorageClass></Contents>
	  <IsTruncated>true</IsTruncated><NextContinuationToken>next-page</NextContinuationToken>
	</ListBucketResult>`
	cfg, client := fake(t, answer)
	listing, err := client.List(context.Background(), cfg, keys, "pack-dev", "photos/", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Folders) != 1 || listing.Folders[0] != "user/" {
		t.Fatalf("folders: %v", listing.Folders)
	}
	// Two Contents came back and one of them was the prefix itself.
	if len(listing.Objects) != 1 || listing.Objects[0].Key != "photos/avatar.webp" {
		t.Fatalf("objects: %+v", listing.Objects)
	}
	if listing.Objects[0].ETag != "abc" || listing.Objects[0].Size != 13046 {
		t.Fatalf("object: %+v", listing.Objects[0])
	}
	if listing.Cursor != "next-page" {
		t.Fatalf("cursor was %q — a truncated page that says it is finished loses objects", listing.Cursor)
	}
}

func TestAListedRequestIsSignedAndScoped(t *testing.T) {
	cfg, client := fake(t, `<ListBucketResult></ListBucketResult>`)
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.Write([]byte(`<ListBucketResult></ListBucketResult>`))
	}))
	defer server.Close()
	cfg.Endpoint = server.URL
	if _, err := client.List(context.Background(), cfg, keys, "pack-dev", "a b/", ""); err != nil {
		t.Fatal(err)
	}
	auth := seen.Header.Get("Authorization")
	for _, want := range []string{"AWS4-HMAC-SHA256", "Credential=AKIAEXAMPLE/", "/auto/s3/aws4_request", "Signature="} {
		if !strings.Contains(auth, want) {
			t.Fatalf("authorization %q is missing %q", auth, want)
		}
	}
	if seen.Header.Get("X-Amz-Content-Sha256") == "" || seen.Header.Get("X-Amz-Date") == "" {
		t.Fatal("a signed request must carry the date and the payload hash it signed over")
	}
	// The space in the prefix has to travel as %20 in both the signature and
	// the URL, or the two are over different strings.
	if !strings.Contains(seen.URL.RawQuery, "prefix=a%20b%2F") {
		t.Fatalf("query was %q", seen.URL.RawQuery)
	}
}

// The link is what a browser follows, so every part of it has to be in the
// query rather than in a header — and it has to expire.
func TestALinkCarriesItsOwnSignatureAndExpiry(t *testing.T) {
	cfg := Config{Endpoint: "https://acct.r2.cloudflarestorage.com", Region: "auto"}
	link, err := New().Link(cfg, keys, "pack-dev", "user/019f/avatar file.webp", 5*time.Minute,
		time.Date(2026, 8, 15, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "acct.r2.cloudflarestorage.com" {
		t.Fatalf("host was %q", parsed.Host)
	}
	// The space is escaped and the separators are not.
	if parsed.EscapedPath() != "/pack-dev/user/019f/avatar%20file.webp" {
		t.Fatalf("path was %q", parsed.EscapedPath())
	}
	query := parsed.Query()
	if query.Get("X-Amz-Expires") != "300" {
		t.Fatalf("expiry was %q — a link that never expires is a key", query.Get("X-Amz-Expires"))
	}
	if query.Get("X-Amz-Signature") == "" || query.Get("X-Amz-Date") != "20260815T020000Z" {
		t.Fatalf("query was %v", query)
	}
	if got := query.Get("X-Amz-Credential"); got != "AKIAEXAMPLE/20260815/auto/s3/aws4_request" {
		t.Fatalf("credential scope was %q", got)
	}
}

// S3 says what went wrong in the body, and the difference between a wrong
// secret and a wrong region is only in there.
func TestAnErrorKeepsTheStoragesOwnSentence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`<Error><Code>SignatureDoesNotMatch</Code><Message>bad signature</Message></Error>`))
	}))
	defer server.Close()
	_, err := NewFor(server.Client()).List(context.Background(),
		Config{Endpoint: server.URL, Region: "auto"}, keys, "pack-dev", "", "")
	if err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("error was %v", err)
	}
}

func TestNoKeysIsItsOwnSentence(t *testing.T) {
	cfg, client := fake(t, "")
	_, err := client.List(context.Background(), cfg, Keys{}, "pack-dev", "", "")
	if err == nil || !strings.Contains(err.Error(), "no S3 credentials") {
		t.Fatalf("error was %v", err)
	}
}
