SHELL := /bin/sh
.PHONY: fmt-check test vet build build-linux-amd64 build-linux-arm64

fmt-check:
	@test -z "$$(gofmt -l cmd internal)"

test:
	go test ./...

vet:
	go vet ./...

build:
	go build -trimpath -o bin/easyhostagent ./cmd/easyhostagent

build-linux-amd64:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/easyhostagent-linux-amd64 ./cmd/easyhostagent

build-linux-arm64:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/easyhostagent-linux-arm64 ./cmd/easyhostagent
