# Benchmarking

A benchmark runs a project's own body of work, its sessions on upool slots, and records what the
pool did under it. It answers what the spike could not: the measurements
[docs/design.md, "What remains unverified"](design.md#what-remains-unverified) leaves open, such as
Editor memory under real use, larger Libraries and more than three Editors, against the budget in
[docs/contracts.md (h) RAM budget](contracts.md#h-ram-budget). upool's own numbers, the step
timings every slot and Editor operation reports and logs
([docs/contracts.md (e), the timings log](contracts.md#e-state-schemas)), are read from that log,
not measured again. Each record adds one more machine, one more project and one more workload, so
the numbers accumulate across runs, and what they change for upool comes from measured use, as the
spike report's "what this changes" sections did
([docs/spike-report.md](spike-report.md#6-what-this-changes-in-the-design)).

## How a benchmark is run

The `upool-benchmark` skill, at
[skills/upool-benchmark/SKILL.md](../skills/upool-benchmark/SKILL.md), is the process: what to
describe, what to measure and how, how to run the sessions, and the scrub before anything leaves
the project. The agent that orchestrates a project's sessions reads it, from outside every slot.
That is why it is a skill of its own, installed into the project with
`npx skills add weslo/upool --skill upool-benchmark`, rather than the `upool` skill the hooks give
a session in a slot, which never reaches that agent. `upool init` does not write it, and the
binary does not embed it (docs/contracts.md A43; #230).

## Where records live

One file per run under `docs/benchmarks/`, named by date and a short slug, such as
`2026-11-10-three-sessions-shared-prefab.md`, added by pull request. A record names no project,
repository, issue, asset, machine or person; it describes each by shape, as the template's fields
ask. The first run is a project's own job, planned in that project, and lands as the first record.
It is also the skill's first test.

## The run record

Copy the template below into the record and fill every field. A measurement the run did not take
says "not measured" and why, rather than staying blank. Times are in seconds unless a field says
milliseconds; memory in GB; upool's step timings as the log gives them, in milliseconds.

````markdown
# <date>: <slug>

## Run

| Field | Value |
|---|---|
| Date | |
| upool | from `upool --version` |
| Unity Editor | the project's Editor version |
| Unity CLI | from `unity --version` |
| Claude Code | from `claude --version` |

## Machine class

| Field | Value |
|---|---|
| Chip | such as Apple M1 Max |
| Memory | such as 64 GB |
| Disk | kind (SSD or other) and file system (APFS or other) |

## Project size class

| Field | Value |
|---|---|
| Library size | such as 6.5 GB |
| Library files | such as 91,000 |
| Assets | source assets under `Assets/`, `.meta` files not counted |

## Workload shape

<how many sessions and their start order; which edit the same files and of what kind; which need
a second peer and which host a server; in what order their changes land>

## Measurements

### Time to ready, per slot

| Slot | Session start to Editor ready | `open`'s `ready` step, when `upool open` opened it |
|---|---|---|
| | | |

### Probe latency

| Editors up | Call 1 | Call 2 | Call 3 |
|---|---|---|---|
| 1 | | | |
| all (<n>) | | | |

### Memory per Editor

Budget: `editor_ram_budget_gb` <n>, `max_editors` <n>. Refused: <which opens, naming the limit, or
none>. Queued: <which opens waited behind a cold import, and for how long, or none>.

| Editor | All ready | Midway | Before the end |
|---|---|---|---|
| | | | |

### The shared files

<for each file two sessions edited: what each session did when it reached it, and when the later
change was confirmed to land, whether git or Unity reported a conflict, who resolved it and how
long that took>

### Second peers

<for each session that needed a second peer: whether the peer ran while the session's own Editor
and the other sessions' were, or what refused or delayed it>

### upool's step timings

| Operation | Slot | Steps (name: ms) | Total ms |
|---|---|---|---|
| | | | |

## Observations

<what happened that the fields above do not hold: lines upool printed, retries, timeouts, hands
needed, anything that surprised>

## What this changes for upool

<each default, rule or open question the run bears on, with the call it suggests and why, as the
spike report's "what this changes" sections do; or "nothing", with why>
````
