.PHONY: all generate run test clean

MODULE := github.com/mirairoad/guard

all: generate
	go build -o guard .

generate:
	go run github.com/mirairoad/howl-go/core/cmd/fsroutes -module $(MODULE)/client/pages
	go tool templ generate

run: all
	./guard

test: generate
	go test ./...

clean:
	rm -f guard
