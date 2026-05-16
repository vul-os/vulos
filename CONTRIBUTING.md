# Contributing to Vula OS

Thanks for being here. Vula OS is a small project with a large surface area, so we keep contributions tightly scoped: one task, one branch, one PR. This document is the practical guide for working that way.

If you just want to get the project running, that lives in [`DEVELOPMENT.md`](DEVELOPMENT.md).

---

## TL;DR

1. Pick an unblocked task from [`tasks.md`](tasks.md).
2. Branch as `task/<ID>` (e.g. `task/SEC-E`).
3. Implement the task's **Scope**, tick its **Acceptance criteria**, run the relevant build.
4. Open a PR. Keep the diff focused on that one task.

That's it. Everything below is detail.

---

## Where to find work

[`tasks.md`](tasks.md) is the source of truth for "what's open". Open it and scroll to the **At-a-glance** table at the top — it lists every area (AI, STREAM, NET, INIT, PEER, SECURITY, …) with a done/total count and a link to the highest-priority remaining task in that area.

A task is yours to pick up if:

- Its status token reads `` `todo` `` (compact form) or `- **Status:** todo` (verbose form).
- Every ID listed under `Depends on` / `dep:` has status `done`.
- Nobody has an open PR with the matching branch name `task/<ID>` (check the PR list).

Priorities (`P0` highest, `P3` lowest) and effort sizes (`S` / `M` / `L`) are advisory. P0s are usually security or foundation work that other tasks depend on — pick those first if you're looking to be load-bearing.

If the area you care about has no `todo` tasks, that's a great signal to read the corresponding `roadmap/<AREA>.md` document and propose new tasks (see "Writing a new task" below).

### What `parallel: no` means

Most tasks are marked `parallel: yes` or `parallel: no`. This is a hint left over from the autonomous orchestrator, which used to run many tasks in parallel worktrees: `no` means the task touches a "hot" shared file (`backend/cmd/server/main.go`, `src/core/AppRegistry.js`, `backend/services/stream/pool.go`, etc.) and shouldn't be merged at the same time as another task touching the same file.

For human contributors this is mostly informational: if you're working on a `parallel: no` task, just rebase on `main` before opening your PR to avoid surprises.

---

## How a task is structured

Tasks come in two formats. Both are equally valid; both are machine-parseable. Don't change the format of an existing task — just polish what's inside.

### Compact form

```
### [SEC-E] webproxy DNS-rebinding + TLS-verify (H4)
`todo` · P1 · M · dep: none · parallel: yes — backend/services/webproxy/proxy.go
Scope: resolve host ONCE and dial the validated pinned IP …
AC: [ ] single-resolution dial [ ] fail-closed on bad resolve [ ] TLS verified …
```

### Verbose form

```
### [AI-05] Make saved AI apps appear in the app launcher with icons and categories
- **Status:** todo
- **Priority:** P1
- **Effort:** M
- **Roadmap:** roadmap/AI.md § AI Apps (persistence, icons, categories)
- **Depends on:** none
- **Parallel-safe:** no — modifies src/core/AppRegistry.js …
- **Context:** …
- **Scope:** …
- **Acceptance criteria:**
  - [ ] …
- **Key files:** …
```

The line **directly below** the `### [ID]` header is the status token. Tooling reads it. Don't move it.

---

## The work loop

```
git checkout main
git pull
git checkout -b task/<ID>          # e.g. task/SEC-E

# … edit, build, test …

go build ./...                      # backend changes
npm run build                       # frontend changes
go test ./backend/...               # if you touched Go code
                                    # (run the targeted subtree if the full
                                    # test set is too slow on your laptop)

git commit -m "task/<ID>: short summary"
git push -u origin task/<ID>
# open PR against main
```

A few unspoken rules:

- **Tick the AC checkboxes** in your PR description (copy them out of the task). It makes review fast.
- **Keep the diff to the task.** If you spot a tangential bug, file a follow-up task instead of fixing it in the same PR. Drive-by fixes are friendly in small projects and painful in big ones; we've grown into the second category.
- **Don't reformat unrelated files.** The repo doesn't enforce a formatter on touch — only on changed lines.
- **Don't push to `main`.** Even maintainers branch. The orchestration tooling assumes `main` is a clean linear history of merged task branches.

---

## Writing a new task

If you've found work that isn't in `tasks.md` and should be — for example, you read `roadmap/NOTIFICATIONS.md` and notice a phase that isn't decomposed yet — add a task. The minimum viable task looks like this:

```
### [AREA-NN] <short title>
`todo` · P2 · M · dep: none · parallel: yes — path/to/main/file.go
Scope: one paragraph describing what to do, with file:line references where useful.
AC: [ ] first measurable outcome [ ] second outcome [ ] build/test command passes
```

Conventions:

- **ID format:** `<AREA>-<NUMBER>` where `<AREA>` is an existing area prefix (`AI`, `STREAM`, `NET`, `CLUSTER`, `INIT`, `BMINIT`, `NOTIF`, `DEVPROF`, `GAME`, `WEBAPP`, `APPSTORE`, `PEER`, `AUTH`, `FED`, `MOBILE`, `MISC`, `SEC`, `LADYBIRD`). Pick the next free number in that area. IDs must be globally unique within the file.
- **Status token must be the very next line** after the header (`` `todo` `` or `` `done` ``). Tooling depends on this.
- **Scope is for the implementer.** Be specific about files and behavior. "Make notifications nicer" is not a task; "Add a `priority` field to `notify.Notification` with values `low|normal|high|critical`, default `normal`; map legacy `level` to it" is.
- **AC must be checkable.** "Works" is not a criterion. "`go test ./backend/services/notify/...` passes" is.
- Append the task at the end of its area section in `tasks.md`. Don't renumber existing entries — IDs are forever.

---

## Where decisions live

If you're about to make a design call that affects more than one task — a new dependency, a security trade-off, a directory layout change — read [`decisions.md`](decisions.md) first to see whether the question has been answered, and if not, propose the answer in your PR description. We'll fold the accepted answer back into `decisions.md` as a new `D##` entry on merge.

The doc has two kinds of entries:

- **`D##` entries** — terse, dated, written by the autonomous orchestrator while it was driving the roadmap. They explain *why* the codebase looks the way it does. Most are still active rules.
- **Verbose sections** — written during human-author sessions when a topic needed more than a paragraph.

---

## Security disclosure

**Please do not file security issues as public GitHub issues.**

If you've found a security problem in Vula OS — anything that lets one user reach another user's data, escape the sandbox, run code as a different user, or bypass auth — email **security@vulos.org** (or, until that mailbox exists, message the repo owner directly).

Include:

- A short description of the issue.
- A minimal reproduction if you have one.
- Your assessment of severity (critical / high / medium / low) and why.

You'll get an acknowledgement within 72 hours. We aim to ship a fix or mitigation within 14 days for critical/high issues; we'll keep you in the loop.

For context, the `SEC-*` tasks in `tasks.md` and decisions `D24`–`D29` document the most recent security pass — that's the kind of audit + remediation flow we want for any new finding.

---

## A small note on tone

The project is open by design — that's literally what *vula* means in isiZulu. We try to keep PRs and issues short, specific, and friendly. If a review comment feels harsh, it's almost certainly tiredness rather than judgement; ask for clarification and we'll rephrase.

Welcome aboard.
