# Autonomous Roadmap Orchestration — Decisions Log

This is an **automated session**. No questions are asked of the user. Every fork-in-the-road
decision is made autonomously (with Opus where it matters) and recorded here with rationale.

## Operating policy

- **Goal:** drive the `roadmap/` toward completion by continuously running Sonnet coding agents
  against tasks in `tasks.md`.
- **Fleet:** target ~10 concurrent Sonnet worker agents; **hard cap 15** total concurrent agents
  (Sonnet workers + any Opus breakdown agents combined).
- **Cadence:** orchestrator wakes every 15 min via cron job `026d80d9`.
- **Deadline:** started 2026-05-16 00:52 +0200. **Hard stop 2026-05-16 04:52 +0200** (4h).
  At/after deadline: stop spawning, let in-flight agents finish, do a final merge/consolidation,
  `CronDelete 026d80d9`, write a final summary, stop.
- **Isolation model:** every code-modifying Sonnet worker runs in its **own git worktree**
  (`isolation: "worktree"`) and commits to a branch `task/<TASK-ID>`. Worktrees share `.git`,
  so committed branches are visible from the main repo. The orchestrator merges clean branches
  into `main` during wake cycles.
- **Collision avoidance:** never run two concurrent workers whose `Key files` overlap. Hot shared
  files (`backend/cmd/server/main.go`, `backend/services/stream/pool.go`, `stream.go`,
  `src/core/AppRegistry.js`) are serialized — at most one in-flight task touching each at a time.
- **Task selection each wake:** pick `Status: todo` tasks whose `Depends on` are all `done` and
  whose `Key files` don't collide with an in-flight task, highest priority (P0→P3) first.
- **Resource guardrails (checked every wake):** if any breached, shrink fleet / pause spawning:
  - disk free on `/System/Volumes/Data` < 20 GB → stop spawning, prune worktrees.
  - memory free < 15% → cap fleet at 5.
  - 1-min load average > 2.5 × cores (cores=8 → >20) → cap fleet at 5.
- **Running low on tasks:** when fewer than ~6 actionable `todo` tasks remain, dispatch Opus
  agents to break down not-yet-decomposed roadmap files into more `tasks.md` entries.

## Decision log

### D1 — 2026-05-16 00:52 — Loop mechanism
Used `/loop 15m` → cron `*/15 * * * *` (session cron `026d80d9`) rather than ScheduleWakeup,
because the cadence is fixed (15 min) not event-gated. Self-terminate after 4h since cron itself
would otherwise run 7 days.

### D2 — 2026-05-16 00:52 — Preserve completed Opus breakdown
The first Opus breakdown agent (AI.md + STREAMING-OPTIMIZATIONS.md) completed before the user
interrupted; its 13 tasks were high quality. Decision: keep that output verbatim as the seed of
`tasks.md` rather than regenerate, to avoid wasting the work.

### D3 — 2026-05-16 00:52 — Worktree isolation + per-task branches
Chose worktree isolation + `task/<ID>` branches + orchestrator-merges over (a) shared-dir
parallel edits (chaotic, guaranteed conflicts on `main.go`) and (b) fully serial single-agent
(too slow for the requested 10-wide fleet). Trade-off: orchestrator must merge branches and may
defer hard conflicts — acceptable for an automated run; deferred conflicts are logged here.

### D4 — 2026-05-16 00:52 — Iteration 1 fleet shape
Iteration 1: 6 Opus breakdown agents (remaining roadmap areas) + 5 Sonnet workers on
disjoint, no-dependency, parallel-safe AI/STREAM tasks = 11 agents (≤15 cap). Ramp Sonnet to
~10 next wake once `tasks.md` is fully populated. Rationale: don't spawn many workers before the
full task list exists (risk of duplicate/colliding work).

### D5 — 2026-05-16 00:5x — "add 5 more agents" vs 15 cap
User asked for +5 agents while 11 were live (would be 16 > hard cap 15). Also only 3
collision-free, no-dependency tasks remained in the AI/STREAM pool (others share hot files
`gpu.go`/`main.go` with in-flight work). Decision: add 3 now (→14 agents, under cap),
honor the "more parallelism" intent by auto-ramping to full ~10–15 worker fleet next wake
once Opus breakdown agents return ~6 new task areas (huge disjoint pool). Cap respected;
resources healthy (76% mem free, load 3.24/8 cores).

### D6 — 2026-05-16 01:0x — Status tracking model
To avoid edit churn/races on `tasks.md`, the live `in_progress` state is tracked only in the
ledger below. `tasks.md` Status is updated only on terminal transitions (→ done / → blocked)
when the orchestrator merges or parks a branch. Source of truth for "what's running now" =
this ledger; source of truth for "what's left" = `tasks.md` todo entries.

### D7 — 2026-05-16 01:0x — Merge policy confirmed working
First 4 worker branches (STREAM-07/02, AI-10, STREAM-05) merged `--no-ff` into main with
zero conflicts (disjoint files, as predicted by the Parallel-safe/Key-files analysis). The
worktree+branch+orchestrator-merge model (D3) is validated. Continue.

## Deferred / unresolved (orchestrator merges, conflicts, blockers)

_(none yet)_

## Assignment ledger

Format: `TASK-ID | branch | agent-status | notes`

- AI breakdown (AI.md+STREAMING) | n/a | done | seeded tasks.md (13 tasks)
- APPSTORE/WEBAPP breakdown | n/a | running | opus bg
- PEER breakdown | n/a | running | opus bg
- INIT/BMINIT breakdown | n/a | running | opus bg
- CLUSTER/NET breakdown | n/a | running | opus bg
- NOTIF/DEVPROF/GAME/MISC breakdown | n/a | running | opus bg
- AUTH/FED/MOBILE/LADYBIRD breakdown | n/a | running | opus bg
- STREAM-07 | task/STREAM-07 | DONE/merged | 26b4e6a — Dockerfile, clean
- STREAM-05 | task/STREAM-05 | DONE/merged | 63423e3 — StreamViewer.jsx, clean (pre-existing lint noted)
- AI-10 | task/AI-10 | DONE/merged | 97c9ff8 — Dock.jsx, clean
- STREAM-02 | task/STREAM-02 | DONE/merged | 46a876d — gpu.go, build+gofmt clean
- AI-08 | task/AI-08 | running | sonnet bg — sandbox/
- AI-09 | task/AI-09 | running | sonnet bg — AIFirstRun + App/ShellProvider
- AI-11 | task/AI-11 | running | sonnet bg — DesktopContextMenu + FileManager
- AI-13 | task/AI-13 | running | sonnet bg — main.go (SOLE main.go claimant; serialize others)

CLAIMED HOT FILES (serialize): main.go→AI-13, gpu.go→STREAM-02, FileManager.jsx→AI-11,
ShellProvider.jsx→AI-09, pool.go/stream.go→free.
