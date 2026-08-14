.PHONY: all generate apis wasm run dev seed test clean css

MODULE := github.com/hushkey-app/guard

all: wasm
	go build -o guard .

generate: apis
	go run github.com/mirairoad/howl-go/core/cmd/fsroutes -module $(MODULE)/client/pages
	go tool templ generate

wasm: generate
	GOOS=js GOARCH=wasm go build -o client/public/views.wasm ./wasm
	install -m 644 "$$(go env GOROOT)/lib/wasm/wasm_exec.js" client/public/wasm_exec.js

# Endpoint tree -> route table + typed client. The client compiles for wasm
# too, so a page could call the API with the same types the handlers use.
apis:
	go run github.com/mirairoad/howl-go/core/cmd/fsapis \
		-dir server/apis -module $(MODULE)/server/apis \
		-client client/api/api_gen.go -client-pkg apiclient

run: all
	./guard

# Post an hour of plausible telemetry to a running instance, over OTLP exactly
# as an exporter would. For the state a new instance starts in: empty, with no
# way to tell whether the panels are broken or the data is simply not there.
# Override with ENDPOINT=, REQUESTS=, WINDOW=, TOKEN=.
seed:
	go run ./dev/seed \
		-endpoint $(or $(ENDPOINT),http://localhost:4318) \
		-requests $(or $(REQUESTS),900) \
		-window $(or $(WINDOW),1h) \
		-token "$(TOKEN)"

# Watch, rebuild, restart, reload the browser. The port stays up across
# restarts; a failed build keeps the last good binary serving and shows the
# compiler error in the browser.
dev:
	go run github.com/mirairoad/howl-go/core/cmd/howl dev -addr :4318 \
		-pre "go run github.com/mirairoad/howl-go/core/cmd/fsapis -dir server/apis -module $(MODULE)/server/apis -client client/api/api_gen.go -client-pkg apiclient"

# The only Node in the project, and the only target that needs it: Tailwind
# compiling client/styles/app.css into the committed client/public/app.css.
# Run it BEFORE `make`, not after: the binary embeds client/public, so a css
# rebuild that follows the go build is a stylesheet the server does not serve.
# `make`, `make dev`, `go test` and `docker build` all read the committed
# bundle, so run this only after using a Tailwind class no source used before.
# The generated sources file carries the module-cache path of the pinned
# shadcn-templ, which differs per machine and is therefore not committed.
css:
	@npm install --no-save --no-package-lock --prefix .howl/tailwind \
		tailwindcss@4.1.18 @tailwindcss/cli@4.1.18 >/dev/null
	@SHADCN_TEMPL_PATH="$$(go list -mod=mod -m -f '{{.Dir}}' github.com/axadrn/shadcn-templ/v2)"; \
	printf '%s\n' \
		"@import \"$$SHADCN_TEMPL_PATH/assets/css/tw-animate.css\";" \
		"@import \"$$SHADCN_TEMPL_PATH/assets/css/shadcn-tailwind.css\";" \
		"@import \"$$SHADCN_TEMPL_PATH/assets/css/styles/style-nova.css\" layer(base);" \
		'@source "../pages/**/*.templ";' \
		'@source "../ui/**/*.templ";' \
		'@source "../public/guard.js";' \
		'@source "../public/core.js";' \
		'@source "../public/charts.js";' \
		'@source "../public/views.js";' \
		'@source "../public/cluster.js";' \
		'@source "../public/registries.js";' \
		'@source "../public/cloud.js";' \
		'@source "../public/storage.js";' \
		'@source "../public/members.js";' \
		"@source \"$$SHADCN_TEMPL_PATH/components/**/*.templ\";" \
		> client/styles/app.sources.css
	.howl/tailwind/node_modules/.bin/tailwindcss -i client/styles/app.css -o client/public/app.css --minify

test: generate
	go test ./...

clean:
	rm -f guard client/public/views.wasm client/public/wasm_exec.js
