.PHONY: all deps assets generate run test clean

MODULE := github.com/mirairoad/guard

all: assets generate
	go build -o guard .

deps:
	npm install

assets:
	npm run css:build

generate:
	go run github.com/mirairoad/howl-go/core/cmd/fsroutes -module $(MODULE)/client/pages
	go tool templ generate

run: all
	./guard

test: generate
	go test ./...

clean:
	rm -f guard client/public/app.css
