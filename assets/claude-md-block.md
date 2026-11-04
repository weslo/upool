## upool

This Unity project is pooled by upool: each agent session works in its own slot, a clone of the
checkout with a warm Library under `.claude/worktrees/slot-N`.

- Take a slot with upool, never with `git worktree add`. `upool take <name>` leases one and prints
  its path; `upool release` hands it back.
- `upool which` prints the path of the slot your working directory is in, and exits 1 outside
  every slot. Run it rather than assume where you are.
- `UNITY_PROJECT_PATH` is exported and names your slot. Pass it positionally to `unity test`,
  `run`, `build`, `close` and `cache key`, which take the project as an argument:
  `unity test "$UNITY_PROJECT_PATH" --mode EditMode`.
- Open your slot's Editor with `upool open`, headless by default for imports, builds and tests, or
  `upool open --gui` for play mode and captures; close it with `upool close`.
- Open the Editor when the work needs it. Once the work is done, a pull request open and nothing
  left to verify, close it with `upool close` rather than leave it running while you wait on
  review: an open Editor holds several GB of memory and one of the few Editors upool admits at
  once. Reopen it with `upool open` for a follow-up.
- Run `unity command save_all` before `unity close "$UNITY_PROJECT_PATH"`.
