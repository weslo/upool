# upool

**Status: Pre-alpha: nothing shipped yet.** No phase of the [build plan](docs/design.md#build-in-four-phases-with-the-clone-recipe-first)
is released; the commands below describe the intended first release.

upool keeps a small pool of pre-warmed Unity workspaces for each project and hands one to every
coding-agent session, so the agent's Unity Editor opens in seconds instead of re-importing the
project for minutes. It returns each workspace to the pool when the session ends and keeps the
number of running Editors within your machine's memory.

## Quickstart

```bash
brew install weslo/tap/upool
upool setup
cd <project> && upool init
```

`upool setup` runs once per machine. `upool init` runs once per Unity project, from Terminal, inside
the repository, with the project's Unity Editor closed.

## How a session gets a slot

- Start a Claude Code session with a worktree in a project you ran `upool init` in; it opens in a
  ready slot under the project's `.claude/worktrees/` in well under a second.
- The slot is a full copy of your checkout, Library and agent files included, so Unity opens it in
  about 10 seconds once warm instead of re-importing everything.
- The session's `unity` commands reach the slot's own Editor, never another session's.
- When you archive the session, the slot goes back to the pool; if it holds uncommitted work, it is
  parked for you to look at, never reset.
- Subagents and repositories without a pool get ordinary git worktrees.

## Requirements

- macOS on an APFS volume
- [Unity CLI](https://docs.unity.com/en-us/unity-cli) 1.0.0-beta.13 or later
- [Claude Code](https://code.claude.com) 2.1.295 or later
- git

## What it does not do

- Run a daemon, GUI, database, MCP server or orchestrator.
- Install Unity Editors or manage licences.
- Hand pool slots to subagents, or rely on Claude Code to remove anything.
- Merge scenes, review code, plan tasks, or auto-commit work in progress.
- Write executable hooks into repositories, or run any from them.
- Update itself; use `brew upgrade upool`.
- Run on Windows or Linux.

## Documentation

- [docs/design.md](docs/design.md): what upool is, why, and the build order.
- [docs/contracts.md](docs/contracts.md): the Claude Code and Unity CLI behaviour upool depends on.
- [docs/spike-report.md](docs/spike-report.md): the measurements behind both.
- [CONTRIBUTING.md](CONTRIBUTING.md): how to build, test and send changes.

## License

[MIT](LICENSE)
