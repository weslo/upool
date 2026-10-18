VERSION ?= dev
LDFLAGS := -X main.version=$(VERSION)

# The commit check-captures compares against. CI sets it to the pull request's base or, on a push to
# main, the commit the push replaced.
BASE ?= origin/main

.PHONY: build test lint check-paths check-captures release-dry-run

build:
	go build -ldflags "$(LDFLAGS)" -o bin/upool ./cmd/upool

test:
	go test ./...

lint: check-paths check-captures
	go vet ./...
	golangci-lint run

# Fails if a tracked file contains a home-directory path or a macOS temporary directory (where
# t.TempDir() lives). Fixtures use variables instead (testdata/README.md).
check-paths:
	@if git grep -nE '/([Uu]sers|home)/[A-Za-z0-9._-]+|-Users-[A-Za-z0-9._]+-|/private/tmp/claude-[0-9]+|/var/folders/[A-Za-z0-9_]+'; then \
		echo "check-paths: machine paths found above; use the variables in testdata/README.md" >&2; exit 1; \
	fi

# Fails if any commit since BASE modified or removed a capture under testdata/captures/. Adding one
# is fine (testdata/README.md). Renames count as a removal plus an addition, so moving a capture out
# of testdata/captures/ fails and moving one in passes.
check-captures:
	@changed=$$(git diff --no-renames --name-only --diff-filter=MDT "$(BASE)...HEAD" -- testdata/captures/) || exit 1; \
	if [ -n "$$changed" ]; then \
		echo "$$changed"; \
		echo "check-captures: captures above were changed or removed since $(BASE); add a new capture instead" >&2; exit 1; \
	fi

release-dry-run:
	goreleaser release --snapshot --clean
