VERSION ?= dev
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test lint fakeunity release-dry-run

build:
	go build -ldflags "$(LDFLAGS)" -o bin/upool ./cmd/upool

test:
	go test ./...

lint:
	go vet ./...
	golangci-lint run

fakeunity:
	@echo "fakeunity: not yet (test/fakeunity arrives in M0)" >&2
	@exit 1

release-dry-run:
	goreleaser release --snapshot --clean
