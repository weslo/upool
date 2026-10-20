---
name: upool
description: How to drive Unity in a project pooled by upool, where each session works in its own slot under .claude/worktrees with its own Editor. Use before running the Unity CLI (unity test, run, build, close, cache key, command), before editing a scene or prefab, after a Unity call times out, and when a subagent needs a Unity project of its own.
---

# Working in a upool slot

A pooled Unity project gives each session a slot: a clone of the checkout, with a warm Library, at
`<repo>/.claude/worktrees/slot-N`. `UNITY_PROJECT_PATH` names your slot. `upool which` prints the
slot your working directory is in, and exits 1 outside every slot.

## Target your own slot

- `unity command`, `status` and `recompile` follow `UNITY_PROJECT_PATH`. Never unset it: with one
  Editor running and no variable, a call goes to that Editor, whichever slot it is in.
- `unity test`, `run`, `build`, `close` and `cache key` ignore the variable. Pass it as their
  project argument; without one they fall back to the working directory, or fail with "Not a
  Unity project":

  ```bash
  unity test "$UNITY_PROJECT_PATH" --mode EditMode
  unity cache key "$UNITY_PROJECT_PATH"
  ```

## Save, then close

Run `unity command save_all` before `unity close "$UNITY_PROJECT_PATH"`. `close` exits without
saving and without asking, so unsaved scene changes are lost.

## Scenes

- Never hand-edit scene or prefab YAML (`*.unity`, `*.prefab`) while a live Editor is reachable for
  the slot. Make the change through the Editor, or save and close it first.
- After a headless session, open your scene explicitly. A batch run leaves the Editor's last scene
  list empty, so the next Editor opens on an Untitled scene rather than yours.

## After a timeout

A Unity call that times out may still have run, especially the first call to a newly opened Editor.
Re-query before retrying: check for the change it was to make (the file, the object, the state)
with `unity status` or a read-only `unity command eval`, and retry only if it is not there.

## Subagents

A subagent works in a plain git worktree, not a slot. One that needs Unity takes a slot of its own
and releases it itself. From anywhere in the repository:

```bash
upool take <branch>     # prints only the slot's path; <branch> is created if it does not exist
```

Use that path on every Unity call, `UNITY_PROJECT_PATH=<slot> unity command …` and
`unity test <slot> …`, since a shell variable may not last from one command to the next. Commit
your work to the branch, then `upool release <slot>`: release resets a clean slot for the next
session and parks a dirty one.
