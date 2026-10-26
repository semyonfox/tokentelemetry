.DEFAULT_GOAL := build
.PHONY: build test check run packages release publish

build:
	cd engine && go build -trimpath -o ../dist/ ./cmd/tokentelemetry ./cmd/tt

test:
	cd engine && go test ./...

check:
	cd engine && go vet ./...

run:
	cd engine && go run ./cmd/tokentelemetry $(ARGS)

packages:
	node engine/scripts/build-npm.mjs

# make release TAG=v0.1.0 builds archives and checksums into dist/release
release:
	engine/scripts/release.sh $(TAG)

# make publish TAG=v0.1.0 also uploads them; push the tag first
publish:
	engine/scripts/release.sh $(TAG) --publish
