# upool

[![ci](https://github.com/weslo/upool/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/weslo/upool/actions/workflows/ci.yml?query=branch%3Amain)

**Status: pre-alpha.** v0.3.0, the pool core, its Claude Code integration and Editor governance,
is on [GitHub Releases](https://github.com/weslo/upool/releases); Homebrew comes with M4. The
[roadmap](#-roadmap) shows where the build stands.

To try v0.3.0 now, download the archive for your Mac with `gh` or `curl`; a browser quarantines the
download, and the binary is not signed. Put the `upool` in it on your `PATH`, then follow the
Quickstart from `upool init` on:

```bash
gh release download v0.3.0 -R weslo/upool -p 'upool_0.3.0_darwin_arm64.tar.gz'   # or darwin_amd64
tar -xzf upool_0.3.0_darwin_arm64.tar.gz upool
```

upool keeps a small pool of pre-warmed Unity workspaces for each project and hands one to every
coding-agent session, so the agent's Unity Editor opens in seconds instead of re-importing the
project for minutes. It returns each workspace to the pool when the session ends and keeps the
number of running Editors within your machine's memory.

## 🚀 Quickstart

```bash
brew install weslo/tap/upool
cd <project> && upool init
```

`upool init` runs once per Unity project, from Terminal, inside the repository, with the project's
Unity Editor closed. It installs the skill and the instruction blocks, makes the project a pool and
clones its first slot from your checkout, then registers upool's hooks, all in that project alone,
and touches no other repository. Upgrade with `brew upgrade upool`, then run `upool init` again in
each project to refresh them.

## 🏊 How a session gets a slot

- Start a Claude Code session with a worktree in a project you ran `upool init` in; it opens in a
  ready slot under the project's `.claude/worktrees/` within a couple of seconds, and the pool is
  topped up in the background. With every slot taken, it waits up to 20 seconds for one, then fails
  to start, saying to run `upool refresh`.
- The slot is a full copy of your checkout, Library and agent files included, so Unity opens it in
  well under a minute instead of re-importing everything.
- The session's `unity` commands reach the slot's own Editor, never another session's.
- When you archive the session, the slot goes back to the pool; if it holds uncommitted work, or a
  commit no branch holds, it is parked for you to look at, never reset.
- Subagents and repositories without a pool get ordinary git worktrees. Once a subagent's session
  is gone, `upool prune` lists its worktree and `upool prune --yes` removes it, unless it holds
  work.

## 📋 Requirements

- macOS on an APFS volume
- [Unity CLI](https://docs.unity.com/en-us/unity-cli) 1.0.0-beta.13 or later
- [Claude Code](https://code.claude.com) 2.1.295 or later
- git

## 🧭 Roadmap

After the bootstrap and a planning pass, the build runs in five milestones, M0 to M4, in the order
[docs/design.md](docs/design.md#build-in-four-phases-with-the-clone-recipe-first) sets. Each
milestone ends with a manual gate run on a real Mac, and its version is tagged only once that gate
passes.

| Status | Milestone | What it delivers | Release |
|---|---|---|---|
| ✅ Done | Bootstrap ([#1](https://github.com/weslo/upool/pull/1), [#7](https://github.com/weslo/upool/pull/7)) | The repository: design documents, spike captures, CI and the release pipeline | — |
| ✅ Done | Planning ([#2](https://github.com/weslo/upool/issues/2), [#10](https://github.com/weslo/upool/pull/10)) | Each milestone below split into issues, listed in the [Build plan](https://github.com/weslo/upool/issues/107) | — |
| ✅ Done | [M0 Scaffold](https://github.com/weslo/upool/milestone/1) | Foundations with no user-facing commands: config, state files under one lock, the output package and the test harness | — |
| ✅ Done | [M1 Pool core](https://github.com/weslo/upool/milestone/2) | `init`, `take`, `release`, `which`, `path`, `status` and `doctor`, usable from Terminal with no agent | v0.1.0 |
| ✅ Done | [M2 Claude Code integration](https://github.com/weslo/upool/milestone/3) | `init`'s per-project hooks that hand each session a slot, the background top-up, the skill and `run` | v0.2.0 |
| ✅ Done | [M3 Editor governance](https://github.com/weslo/upool/milestone/4) | The RAM budget for running Editors, `open` and `close`, and the lane hook | v0.3.0 |
| 🚧 In progress | [M4 Hygiene and release](https://github.com/weslo/upool/milestone/5) | `refresh --fetch`, `prune`, `status --du` and the disk budget, then the Homebrew release the Quickstart uses | v0.4.0 |

## 📚 Documentation

- [docs/design.md](docs/design.md): what upool is, why, and the build order.
- [docs/contracts.md](docs/contracts.md): the Claude Code and Unity CLI behaviour upool depends on.
- [docs/spike-report.md](docs/spike-report.md): the measurements behind both.
- [CONTRIBUTING.md](CONTRIBUTING.md): how to build, test and send changes, and the coding standards.
- [AGENTS.md](AGENTS.md): what a coding agent working on this repository must know.

## 📄 License

[MIT](LICENSE)
