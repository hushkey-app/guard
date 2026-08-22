// Package vaultproxy puts the secrets endpoints on guard's own port.
//
// There are two doors to the same secrets and they are for two different
// callers:
//
//   - **`guard-vault` on :4319** is the one that matters. It is a second
//     process sharing nothing with guard but a database file and a key file,
//     deployed and restarted separately, so an application asking for its
//     database password at boot is not waiting on the dashboard's release. That
//     is the whole reason the vault is its own binary, and nothing here changes
//     it.
//   - **`/v1/secrets` on guard's port** is for the caller that cannot reach
//     :4319 — an application outside the VPC, or one behind a proxy that
//     terminates one hostname and nothing else. It is a reverse proxy to the
//     first door and not a second implementation of it, so there is exactly one
//     place that checks a key, one place that decides what an environment
//     holds, and one read log.
//
// Off unless switched on, and that is not caution for its own sake. Guard's
// port is usually the published one — a domain, a certificate, a CDN in front —
// and turning this on means a leaked key is usable from the internet rather
// than from inside your network. That is a real widening and it should be a
// decision somebody made, so `GUARD_VAULT_PROXY` starts empty and the log says
// what happened either way.
//
// What it deliberately does not do:
//
//   - **It adds no credential of its own.** The Authorization header travels
//     untouched and the vault answers it. `GUARD_TOKEN` and a session cookie
//     open every other write endpoint in guard and neither opens this one,
//     because guard never inspects the header — it forwards it. A test pins
//     that, since today it is true by construction and construction changes.
//   - **It proxies two routes, not a prefix.** `GET /v1/secrets` and
//     `GET /v1/secrets/{key}` are the vault's whole read surface; forwarding
//     `/v1/` wholesale would hand anything the vault grows later to the public
//     port without anybody choosing it.
package vaultproxy

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Timeout bounds one forwarded read. Everything behind it is an indexed lookup
// and an AES-GCM decrypt of a few kilobytes — the vault's own server allows ten
// seconds for a request, and a proxy that waited longer than the thing it is
// proxying would only be holding a connection open for nothing.
const Timeout = 10 * time.Second

// Config is the switch and where to send it.
type Config struct {
	// Enabled is GUARD_VAULT_PROXY. Empty is off.
	Enabled bool
	// Upstream is where guard-vault answers, as host:port.
	Upstream string
}

// UpstreamFrom turns the vault's *listen* address into an address to dial.
//
// They are not the same string and the difference is the whole of this
// function. `:4319` and `0.0.0.0:4319` mean "every interface" to a listener and
// nothing to a dialer, so both become loopback — which is right, because a
// vault listening on every interface is certainly listening on that one, and
// guard is on the same box. An explicit host is kept: somebody who bound the
// VPC address meant that address.
func UpstreamFrom(listen string) string {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return "127.0.0.1:4319"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		// No port in it at all: treat the whole thing as a host.
		return net.JoinHostPort(listen, "4319")
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// Register puts the two routes on the mux, or explains why it did not.
//
// It returns an error only for a configuration that cannot work; a disabled
// proxy is not an error and says so at info, because "why is /v1/secrets a 404"
// should be answerable from the boot log.
func Register(mux *http.ServeMux, cfg Config) error {
	if !cfg.Enabled {
		slog.Info("the secrets proxy is off — /v1/secrets answers on the vault's port only",
			slog.String("fix", "GUARD_VAULT_PROXY=1 to also serve it here"))
		return nil
	}
	upstream := cfg.Upstream
	if upstream == "" {
		return errors.New("the secrets proxy needs to know where guard-vault answers")
	}
	target, err := url.Parse("http://" + upstream)
	if err != nil || target.Host == "" {
		return errors.New("that is not an address guard-vault could be answering on: " + upstream)
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			// The path is set explicitly rather than joined: SetURL would
			// prefix the target's path, and this way the two routes below are
			// the only two that can ever be reached.
			r.Out.URL.Path = r.In.URL.Path
			r.Out.Host = target.Host
			// Deliberately no X-Forwarded-For or X-Forwarded-Host: the vault
			// records the caller's address in its read log, and a header a
			// caller controls is not the address. It sees guard's.
		},
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			ResponseHeaderTimeout: Timeout,
			// One box, one hop. No pooling worth speaking of, and an idle
			// connection to a process that restarts on its own schedule is a
			// connection that fails the request that finds it.
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     30 * time.Second,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// The vault being down is the interesting case and it deserves a
			// sentence rather than a bare 502. It is also *not* an emergency
			// for the caller that can reach :4319 — which is the caller this
			// endpoint does not exist for.
			slog.Warn("the secrets proxy could not reach guard-vault",
				slog.String("upstream", upstream), slog.String("path", r.URL.Path), slog.Any("err", err))
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"guard-vault is not answering on ` + upstream + `"}`))
		},
	}

	mux.Handle("GET /v1/secrets", proxy)
	mux.Handle("GET /v1/secrets/{key}", proxy)
	slog.Info("the secrets proxy is on — /v1/secrets is served here as well as on the vault's port",
		slog.String("upstream", upstream),
		slog.String("note", "a key leaked from here is usable wherever this port is reachable"))
	return nil
}
