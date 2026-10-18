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

# Fails if a tracked file contains a home-directory path. Fixtures use variables instead
# (testdata/README.md).
check-paths:
	@if git grep -nE '/([Uu]sers|home)/[A-Za-z0-9._-]+|-Users-[A-Za-z0-9._]+-|/private/tmp/claude-[0-9]+'; then \
		echo "check-paths: machine paths found above; use the variables in testdata/README.md" >&2; exit 1; \
	fi

fakeunity:
	@echo "fakeunity: not yet (test/fakeunity arrives in M0)" >&2
	@exit 1

release-dry-run:
	goreleaser release --snapshot --clean
