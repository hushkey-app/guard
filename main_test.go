package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mirairoad/howl-go/core/mw"
)

func TestWithoutWebSocketSkipsBufferedMiddlewareForUpgrade(t *testing.T) {
	buffered := mw.Middleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Buffered", "yes")
			next.ServeHTTP(w, r)
		})
	})
	handler := withoutWebSocket(buffered)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))

	request := httptest.NewRequest(http.MethodGet, "/terminal", nil)
	request.Header.Set("Upgrade", "websocket")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSwitchingProtocols)
	}
	if got := response.Header().Get("X-Buffered"); got != "" {
		t.Fatalf("buffered middleware ran for websocket upgrade: %q", got)
	}
}

func TestWithoutWebSocketKeepsMiddlewareForHTTP(t *testing.T) {
	buffered := mw.Middleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Buffered", "yes")
			next.ServeHTTP(w, r)
		})
	})
	handler := withoutWebSocket(buffered)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api", nil))

	if got := response.Header().Get("X-Buffered"); got != "yes" {
		t.Fatalf("buffered middleware header = %q, want yes", got)
	}
}
