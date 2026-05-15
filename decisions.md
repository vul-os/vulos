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
First worker branches merged `--no-ff` zero conflicts (disjoint files, as predicted). D3 model validated.

### D8 — 2026-05-16 01:12 — Baseline commit of pre-existing WIP
Pre-session uncommitted files (appnet manifest.go/store.go, AppRegistry.js) + untracked
bundled apps were blocking merges of tasks touching them and being perpetually deferred.
Decision: commit them as-is in one baseline commit (47d1544) together with tasks.md +
decisions.md, so every worktree branches from a complete clean tree. Preserves the WIP
(no discard/misattribution beyond a commit), unblocks ~8 tasks, makes the merge pipeline
robust. This is the right call for an automated pipeline; repo owner commits WIP casually
(history shows ".", "," messages) so a baseline commit fits the repo's norms.

### D9 — 2026-05-16 01:12 — "Locked files" concept retired
Following D8 the manifest.go/store.go/AppRegistry.js tasks are no longer locked; they are
now governed only by the normal hot-file serialization rule (one concurrent editor each).

### D10 — 2026-05-16 01:12 — Throttle wave to 8 on elevated load
1-min load hit 14.71 (rising) on 8 cores after the agent burst (mem still 69% free, disk fine).
Not breaching the hard cap (>20) but elevated. Decision: launch 8 workers this tick (not 10),
balanced 4 light(frontend/yaml) / 4 backend, and ramp toward ~10 next cron tick if load
recovers. Honors user's resource-safety instruction over the ~10 target when they conflict.

## Full backlog persisted
tasks.md now holds the complete decomposed backlog: AI(13)+STREAM(8) seeded, plus
WEBAPP/APPSTORE(23), NET/CLUSTER(20), INIT/BMINIT(26), NOTIF/DEVPROF/GAME/MISC(25),
future AUTH/FED/MOBILE/LADYBIRD(27), PEER(41). ~183 tasks. All 6 Opus breakdown agents
complete. Source of truth for remaining work = tasks.md `todo` entries.

## Deferred / unresolved (orchestrator merges, conflicts, blockers)

### GAME-04 — DEFERRED (tangled merge conflict) — 2026-05-16 ~01:29
GAME-04 (useGamepad hook refactor) branched from baseline which had STREAM-05's inline
gamepad loop. GAME-03 (merged) retained that inline loop + added pointer-lock. GAME-04
removed `gpLoopRef`+inline loop for the hook. 3-way merge left 2 trivial hunks BUT 6
orphaned `gpLoopRef` refs (GAME-03's retained loop referencing the removed decl) — a
semantic break, clean-text but broken. Per protocol, `git merge --abort` (StreamViewer.jsx
restored to working HEAD = STREAM-05 loop + GAME-03 pointer-lock, NO regression). Branch
`task/GAME-04`/`69a1836` preserved. RE-TASK later: re-implement the useGamepad refactor
fresh on top of current main (which has both STREAM-05 inline loop + GAME-03 pointer-lock),
not via the stale branch. tasks.md GAME-04 left `todo`.

### Resolved conflict pattern (reference for future cycles)
- main.go dup-import (WEBAPP-04): worker re-added an import that already exists → keep only
  the genuinely-new import line, drop the dup, gofmt, `go build`, commit. Build-gate every
  main.go merge.
- app-dir rewrites (WEBAPP-02/03/07): worker rewrote apps/<x>/ that baseline also has →
  `git checkout --theirs -- apps/<x>` (impl supersedes the shell), commit.
- go.mod (CLUSTER-01 + AUTH-01): different require lines auto-merge clean; if conflict,
  `--theirs` then `go mod tidy` + `go build`.

### D13 — 2026-05-16 01:38 — STRATEGIC PIVOT: greenfield-biased waves
ROOT CAUSE confirmed: Agent worktree isolation pins each worker's base at ~session-start
main, NOT current main. So any task touching a file a previously-merged task also touched
conflicts; git auto-merges only when regions differ. Re-tasking a conflicting task from the
same pinned base RE-CONFLICTS — defer+retask is a treadmill for hot files.
DECISION: future waves select almost exclusively GREENFIELD/ISOLATED tasks — brand-new
`apps/<x>/` dirs and brand-new `backend/services/<pkg>/` packages, plus "cold" files no
merged task has touched. These 3-way auto-merge clean regardless of stale base. Hot shared
files (cmd/server/main.go, Setup.jsx, notify.go, Terminal.jsx, stream.go, registry.json,
go.mod, AppRegistry.js, Settings.jsx, App.jsx) are SERIALIZED: ≤1 per wave, or drained by
the orchestrator near the end. WEBAPP new-app workers instructed NOT to edit AppRegistry.js
(dynamic /api/store/installed discovers apps/ dirs) → fully isolated.
Deferred-from-stale-base (re-task only after their hot file is otherwise quiesced, or
orchestrator does them serially): GAME-04, NOTIF-02, INIT-02, AI-12.

### Still open
- NET-06 vs INIT-01 both create instance ULID/identity — share one identity package;
  don't run concurrently (INIT-01 not yet dispatched; NET-06 not yet dispatched).
- D12: future AUTH/FED/MOBILE/LADYBIRD section was missing from tasks.md (initial big
  append covered 5/6 areas). FIXED 01:30 — now persisted (AUTH-01 marked done).

## Assignment ledger

Format: `TASK-ID | branch | agent-status | notes`

All 6 Opus breakdown agents: DONE (backlog in tasks.md).

DONE/merged to main (8): STREAM-07(26b4e6a) STREAM-02(46a876d) AI-10(97c9ff8)
STREAM-05(63423e3) AI-08 AI-11 AI-09 AI-13. tasks.md Status=done for these.

IN-FLIGHT wave (8, launched 01:12, sonnet bg worktrees):
- WEBAPP-02 | task/WEBAPP-02 | apps/pdf-viewer/
- WEBAPP-03 | task/WEBAPP-03 | apps/text-editor/
- WEBAPP-04 | task/WEBAPP-04 | new appfs/ + main.go (main.go owner)
- CLUSTER-01 | task/CLUSTER-01 | new store/ + go.mod (go.mod owner)
- NOTIF-01 | task/NOTIF-01 | notify.go (owner)
- GAME-03 | task/GAME-03 | StreamViewer.jsx + stream.go (owner)
- MISC-02 | task/MISC-02 | Terminal.jsx
- MISC-04 | task/MISC-04 | .github/workflows/

NEXT-WAVE candidates (collision-free, deps met): WEBAPP-07, GAME-01(pool.go),
DEVPROF-01, AUTH-01, AUTH-05, AUTH-09, BMINIT-03, NET-05, NET-07, PEER-01, BMINIT-11.
Avoid concurrent: any 2 touching main.go / go.mod / notify.go / stream.go / pool.go /
manifest.go / Setup.jsx / AppRegistry.js / Settings.jsx / Toasts.jsx.

--- SNAPSHOT 2026-05-16 01:30 (cron cycle) ---
DONE/merged to main: 25 tasks (wave-1 8, wave-2 8, wave-3 9: GAME-01 NOTIF-03 NET-05
DEVPROF-01 BMINIT-11 WEBAPP-07 BMINIT-01 AUTH-01 NET-03). GAME-04 DEFERRED (see above).
tasks.md committed 8f6cbbd. Backlog: ~158 todo, ~183 total. Deadline 04:52 (~3h20m left).
Resources at snapshot: load ~10-12/8 cores, mem 68% free, disk 511Gi. Healthy.
IN-FLIGHT wave-4 (10, launched 01:30, sonnet bg worktrees, branch task/<ID>):
NOTIF-02(notify.go+main.go) GAME-02(pool/bitrate/gpu.go) APPSTORE-01(appnet registry/store.go+registry.json)
DEVPROF-02(Setup.jsx) MISC-01(ThemeProvider/Settings/index.css) AUTH-05(credvault/ new)
CLUSTER-02(auth pkg) INIT-02(Dockerfile/build.sh/cmd-init) AI-12(AskAIButton/FileManager/Terminal)
FED-01(apps/social/ new + AppRegistry.js).
Conflict-resolution playbook recorded in Deferred section above.
--- END SNAPSHOT ---

ORCHESTRATOR PROTOCOL each cron wake:
1. Merge completed task/* branches (`git merge --no-ff`), set tasks.md Status=done, note commit.
2. On merge conflict: skip, log under Deferred, leave branch for later.
3. Resource check (load/mem/disk); apply D-guardrails.
4. Refill to ~10 in-flight from tasks.md `todo` (deps met, no hot-file collision w/ in-flight).
5. When <6 todo remain: (none now — backlog huge).
6. If clock ≥ 04:52 (+2026-05-16): stop spawning, finish in-flight, final merge, CronDelete 026d80d9, summarize, stop.
