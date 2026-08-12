// Package apis is guard's endpoint layer: one file per endpoint, each declaring
// its method, path, roles and the Go types of its query, body and response.
//
// The file's location on disk is its URL — server/apis/summary.api.go serves
// /api/summary — and core/cmd/fsapis turns the tree into apis_gen.go plus a
// typed client. Nothing here registers itself; main.go passes the generated
// table to api.Register with guard's api.Config.
//
// OTLP ingestion is deliberately not here. /v1/logs, /v1/traces and /v1/metrics
// speak protobuf and answer with protobuf, so they stay ordinary handlers in
// internal/ingest — a typed JSON envelope around them would buy nothing.
//
// Endpoints reach the telemetry store through server/apis/store, which is a
// leaf: this package holds the generated table and therefore imports every
// endpoint package, so nothing an endpoint imports may lead back here.
package apis
