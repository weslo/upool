# Contributing

upool is built one GitHub issue at a time against a settled design. Read
[docs/design.md](docs/design.md) and [docs/contracts.md](docs/contracts.md) before changing behaviour.

## Build and test

Requirements: Go 1.27 or later (the version in `go.mod`, which CI uses),
[golangci-lint](https://golangci-lint.run) v2, and [GoReleaser](https://goreleaser.com) v2 for
`release-dry-run` only.

| Command | What it does |
|---|---|
| `make build` | builds `bin/upool` |
| `make test` | `go test ./...` |
| `make lint` | `make check-paths`, `make check-captures`, `go vet ./...` and `golangci-lint run` |
| `make check-paths` | fails if a tracked file contains a home-directory path or a macOS temporary directory |
| `make check-captures` | fails if a commit since `BASE` (default `origin/main`) changed or removed anything under `testdata/captures/` |
| `make release-dry-run` | a GoReleaser snapshot build into `dist/`, publishing nothing |

CI runs both checks, vet, lint and `go test -race -shuffle=on ./...` on `ubuntu-latest` and
`macos-latest` for every pull request and every push to `main`. Tests tagged `//go:build darwin`
(APFS clones, `flock`, the clone recipe) run only on macOS. Tests tagged `//go:build unity` need a
real Editor and never run in CI.

## Testing

- **Call upool in-process.** `main` only calls `run(args, stdin, stdout, stderr, getenv)`, so a test
  calls `run` and checks stdout, stderr and the exit code without building a binary. Command-line
  tests that read better as scripts (a hook capture on stdin, then exactly one path on stdout) use
  [testscript](https://pkg.go.dev/github.com/rogpeppe/go-internal/testscript).
- **The fake `unity` is the test binary.** A package that needs the Unity CLI has `TestMain` act as
  the fake when `filepath.Base(os.Args[0]) == "unity"`, and its tests put a `unity` symlink to
  `os.Args[0]` first on `PATH`. There is nothing separate to build or install.
- **The fake records and replays; it does not imitate Unity.** Each call appends its arguments,
  `UNITY_PROJECT_PATH` and working directory to a log the test reads, then replies with the next
  scripted capture from `testdata/captures/unity/` and its exit code. Targeting rules
  ([docs/contracts.md](docs/contracts.md) (c)) are asserted on the recorded calls: what upool sent,
  not what a simulated Unity would have done with it.
- **Most tests never leave the process.** `internal/unity` and `internal/git` run commands through a
  small runner interface, and unit tests feed capture bytes through it. Only a few end-to-end tests
  go through `PATH`.
- **Real git, always.** Tests run real git in temporary repositories and never fake it.
- **Isolated from this machine.** Every test sets `HOME` to a temporary directory, and resolves
  temporary paths with `filepath.EvalSymlinks` before comparing them (`t.TempDir()` is under
  `/var`, which macOS also spells `/private/var`).
- **No sleeps.** `confirm_timeout`, `take --timeout` and `agent_ttl` are tested with
  `testing/synctest` or an injected clock.
- **Other processes by re-exec.** A test that needs a second process, such as two processes
  contending for the lock, re-executes the test binary the same way the fake `unity` does.
- **Every capture earns its place.** A rule that applies to a whole directory of captures, such as
  hook classification over `testdata/captures/hooks/`, is tested against every file in it.
- **Golden files for upool's own output.** They live in `testdata/golden/` and are regenerated with
  `go test ./internal/<package> -update` (testdata/README.md).

## Rules

1. **Test against fakes and captures.** Every behaviour that touches Unity or Claude Code is tested
   against the fake `unity` and the captures under [`testdata/captures/`](testdata/README.md), as
   [Testing](#testing) describes. Never edit or remove a capture: if parsing has to change, add a new
   capture from a real Editor or session (`scripts/capture-unity.sh`, `scripts/capture-hook.sh`) and
   record its source in `testdata/README.md`; `make check-captures` fails CI otherwise. Replace every
   path in a capture with the variable for its role before committing it (`testdata/README.md` lists
   them); `make check-paths` fails CI if a home-directory path gets in.
2. **Manual gates, not claims.** Anything that can only be verified with a real Unity Editor or the
   Claude desktop app is a manual gate: an exact check (commands, what to look for, in which log)
   listed in `docs/manual-gates/M<n>.md`. A pull request never claims such behaviour works; it says
   "Manual gate still open: yes" and names the check.
3. **One pull request per issue.** Branch `issue-<n>` from `main`, implement exactly what the issue
   says, cite the design section it names, update `CHANGELOG.md` under Unreleased, and fill in the
   pull request template with "Closes #<n>".
4. **CI green, then a human.** A pull request goes to review only once CI passes. A human reviews and
   merges every pull request; reviewers may use agents to help them read a diff, but the decision to
   merge is theirs. Merges are squash merges.

   Branch protection on `main` is deliberately light, for solo agentic work, and stays that way: it
   requires a pull request and the two CI checks, `ci (ubuntu-latest)` and `ci (macos-latest)`, and
   nothing else. There is no required approval (GitHub does not let an author approve their own pull
   request), no requirement that a branch be up to date with `main`, and no enforcement for
   administrators, so the maintainer, and an agent working with the maintainer's credentials, can
   push to `main` directly. Do not tighten these settings or report them as a gap. `CODEOWNERS`
   stays for when reviews are required later. Renaming the CI job or its matrix changes the check
   names, so update the required checks when that change merges.
5. **No new dependencies without an issue.** The design allows a TOML parser, at most one CLI
   framework, and, for tests only, `github.com/rogpeppe/go-internal/testscript`; anything else needs
   an issue that makes the case first.
