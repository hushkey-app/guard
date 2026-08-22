# internal/telemetry/model — data, and nothing else

**Scoped gate:**
`go test ./internal/telemetry/model/ && GOOS=js GOARCH=wasm go build ./internal/telemetry/model/`

**The wasm build is not optional.** The generated API client imports the types
an endpoint declares, and that client has to compile for `js/wasm` so a page
can call the API with the same types the handler validates. Import anything
that opens SQLite — including the parent package — and the failure is a linker
error about pthreads, from a page that only wanted an Event.

So: types and pure functions here, storage above. The parent aliases every
name, so adding a type here costs one alias line there and nothing else.

`Validate() error` is `core/api`'s hook — it runs after the query string is
decoded and before the handler, and a plain error becomes a 400. Refuse rather
than clamp: silently answering 200 with something the caller did not ask for is
harder to debug than being told.
