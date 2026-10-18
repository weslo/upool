# AGENTS.md

Guidance for every coding agent working on this repository. [CLAUDE.md](CLAUDE.md) imports this
file, so Claude Code reads the same text as any other agent: put new guidance here, not there.

upool is a Go command-line tool built one GitHub issue at a time against a settled design. Before
changing anything, read [CONTRIBUTING.md](CONTRIBUTING.md) in full: its commands, coding standards,
test approach and rules bind agents exactly as they bind people. Read
[docs/design.md](docs/design.md) and [docs/contracts.md](docs/contracts.md) before changing
behaviour.

## Working on an issue

- Branch `issue-<n>` from an up-to-date `main` and do what the issue says, nothing more. Something
  real but out of scope goes in a new issue, not in the diff.
- The design is settled. Where an issue or the design leaves a question open, do not answer it
  yourself: comment on the issue with the question and stop. `decision-needed` issues are the
  maintainer's to answer.
- Unity CLI and Claude Code behave in ways their documentation does not always describe. What they
  do is what the captures under `testdata/captures/` and the tables in `docs/contracts.md` record,
  not what you expect or remember.
- Before opening the pull request, run `make build test lint` and `gofmt -l .`; both must pass
  cleanly. `make lint` needs golangci-lint v2 and an up-to-date `origin/main`, which
  `make check-captures` diffs against.
- Update `CHANGELOG.md` under Unreleased and fill in the pull request template: `Closes #<n>`, what
  changed, the captures and commands that verified it, and whether a manual gate is still open.

## Rules that are easy to break

Each is in CONTRIBUTING.md; these are the ones an agent is likeliest to break without noticing.

- **Never edit or remove a capture** under `testdata/captures/`. Add a new one instead (rule 1).
- **No machine paths in a tracked file.** Use the variables in `testdata/README.md`, and run new
  captures through `scripts/scrubpaths`; `make check-paths` fails otherwise.
- **Never claim what only a real Editor or the desktop app can show.** That is a manual gate: name
  the check and say it is still open (rule 2).
- **No new dependencies** without an issue that makes the case first (rule 5).
- **Do not merge on your own initiative.** The maintainer decides every merge (rule 4). Do not
  tighten branch protection either, or report how light it is as a gap.

## This repository's agent files are not upool's

This file and CLAUDE.md are for agents building upool. The instruction block that `upool init`
writes into a Unity project's CLAUDE.md and the skill that `upool setup` installs are part of the
product: they will live under `assets/` (the layout in docs/design.md) and change only through an
issue.
