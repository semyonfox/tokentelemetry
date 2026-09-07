.DEFAULT_GOAL := build
.PHONY: build test check run packages

build:
	cd engine && go build -trimpath -o ../dist/tokentelemetry ./cmd/tokentelemetry

test:
	cd engine && go test ./...

check:
	cd engine && go vet ./...

run:
	cd engine && go run ./cmd/tokentelemetry $(ARGS)

packages:
	node engine/scripts/build-npm.mjs
