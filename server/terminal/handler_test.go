package terminal

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestWithProtocolTokenRestoresBearerAuthorization(t *testing.T) {
	token := "secret with punctuation:!"
	request := httptest.NewRequest(http.MethodGet, "/api/cluster/terminal/7", nil)
	request.Header.Set("Sec-WebSocket-Protocol", protocol+", guard-token."+base64.RawURLEncoding.EncodeToString([]byte(token)))

	authorized := requestWithProtocolToken(request)
	if got := authorized.Header.Get("Authorization"); got != "Bearer "+token {
		t.Fatalf("Authorization = %q, want bearer token", got)
	}
	if got := request.Header.Get("Authorization"); got != "" {
		t.Fatalf("original request was mutated: Authorization = %q", got)
	}
}

func TestRequestWithProtocolTokenIgnoresMalformedToken(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/cluster/terminal/7", nil)
	request.Header.Set("Sec-WebSocket-Protocol", protocol+", guard-token.not+base64")

	if got := requestWithProtocolToken(request).Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q for malformed protocol token", got)
	}
}
