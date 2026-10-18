# Contributing

upool is built one GitHub issue at a time against a settled design. Read
[docs/design.md](docs/design.md) and [docs/contracts.md](docs/contracts.md) before changing behaviour.

## Build and test

Requirements: Go 1.23 or later, [golangci-lint](https://golangci-lint.run) v2, and
[GoReleaser](https://goreleaser.com) v2 for `release-dry-run` only.

| Command | What it does |
|---|---|
| `make build` | builds `bin/upool` |
| `make test` | `go test ./...` |
| `make lint` | `make check-paths`, `go vet ./...` and `golangci-lint run` |
| `make check-paths` | fails if a tracked file contains a home-directory path |
| `make fakeunity` | builds the fake `unity` binary for tests (not yet: arrives in M0) |
| `make release-dry-run` | a GoReleaser snapshot build into `dist/`, publishing nothing |

CI runs vet, lint and the tests on `ubuntu-latest` and `macos-latest` for every push and pull
request. Tests tagged `//go:build darwin` (APFS clones, `flock`, the clone recipe) run only on macOS.
Tests tagged `//go:build unity` need a real Editor and never run in CI.

## Rules

1. **Test against fakes and fixtures.** Every behaviour that touches Unity or Claude Code is tested
   against `test/fakeunity` and the captured fixtures under [`testdata/`](testdata/README.md). Never
   edit a fixture: if parsing has to change, add a new capture from a real Editor or session and
   record its source in `testdata/README.md`. Replace every path in a capture with the variable for
   its role before committing it (`testdata/README.md` lists them); `make check-paths` fails CI if a
   home-directory path gets in.
2. **Manual gates, not claims.** Anything that can only be verified with a real Unity Editor or the
   Claude desktop app is a manual gate: an exact check (commands, what to look for, in which log)
   listed in `docs/manual-gates/M<n>.md`. A pull request never claims such behaviour works; it says
   "Manual gate still open: yes" and names the check.
3. **One pull request per issue.** Branch `issue-<n>` from `main`, implement exactly what the issue
   says, cite the design section it names, update `CHANGELOG.md` under Unreleased, and fill in the
   pull request template with "Closes #<n>".
4. **CI green, then a human.** A pull request goes to review only once CI passes. A human reviews and
   merges every pull request; reviewers may use agents to help them read a diff, but the decision to
   merge is theirs. Branch protection requires a pull request and green CI but no approval, since
   GitHub does not let an author approve their own pull request. Merges are squash merges.
5. **No new dependencies without an issue.** The design allows a TOML parser and at most one CLI
   framework; anything else needs an issue that makes the case first.
