VERSION ?= dev
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test lint check-paths fakeunity release-dry-run

build:
	go build -ldflags "$(LDFLAGS)" -o bin/upool ./cmd/upool

test:
	go test ./...

lint: check-paths
	go vet ./...
	golangci-lint run

# Fails if a tracked file names a real home directory. Captures use /Users/user (testdata/README.md).
check-paths:
	@if git grep -noE '/(Users|home)/[A-Za-z0-9._-]+|-Users-[A-Za-z0-9._]+-' | grep -vE ':(/Users/user|-Users-user-)$$'; then \
		echo "check-paths: local home-directory paths found above; redact them to /Users/user" >&2; exit 1; \
	fi

fakeunity:
	@echo "fakeunity: not yet (test/fakeunity arrives in M0)" >&2
	@exit 1

release-dry-run:
	goreleaser release --snapshot --clean
