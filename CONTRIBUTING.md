# Contributing to Vula OS

Thanks for being here. Vula OS is a small project with a large surface area, so we keep contributions tightly scoped: one task, one branch, one PR. This document is the practical guide for working that way.

If you just want to get the project running, that lives in [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md).

> **First-run gotcha:** cloning only this repo is not enough. `npm install`
> resolves `@vulos/relay-client` from `../vulos-relay/client`, so that sibling
> repo must be cloned beside `vulos/` — and `vulos-relay/client` must be built
> once (`npm install && npm run build:lib`) to produce its `dist-lib/`. See the
> quickstart in [README.md](README.md#develop-with-hot-reload).

---

## TL;DR

1. Pick an unblocked issue from [GitHub Issues](https://github.com/vul-os/vulos/issues) (or open one for the change you want to make).
2. Branch as `fix/`, `feat/`, or `docs/` (e.g. `feat/webproxy-tls-verify`).
3. Implement the change, run the relevant build + tests.
4. Open a PR. Keep the diff focused on one concern.

That's it. Everything below is detail.

---

## Where to find work

[GitHub Issues](https://github.com/vul-os/vulos/issues) is the source of truth for "what's open" — filter by label (area, priority) to find something to pick up. GitHub Projects/Milestones track the larger themes; the high-level direction lives in [ROADMAP.md](ROADMAP.md).

An issue is yours to pick up if it's unassigned, unblocked (any issue it says it depends on is closed), and nobody has an open PR for it. Security and foundation work is usually highest priority — pick those first if you're looking to be load-bearing.

If the area you care about has no open issues, that's a great signal to read the corresponding `roadmap/<AREA>.md` design document and open a new issue proposing the work.

---

## The work loop

```
git checkout main
git pull
git checkout -b feat/<short-name>   # or fix/… , docs/…

# … edit, build, test …

cd backend && go build ./... && cd ..   # backend changes (module root is backend/)
npm run build                       # frontend changes
cd backend && go test ./... && cd ..   # if you touched Go code
                                    # (run the targeted subtree if the full
                                    # test set is too slow on your laptop)
                                    # or just: make build && make test-local

git commit -m "short summary (#<issue>)"
git push -u origin feat/<short-name>
# open PR against main
```

A few unspoken rules:

- **Reference the issue** in your PR description and tick any acceptance checkboxes it lists. It makes review fast.
- **Keep the diff focused.** If you spot a tangential bug, file a follow-up issue instead of fixing it in the same PR. Drive-by fixes are friendly in small projects and painful in big ones; we've grown into the second category.
- **Don't reformat unrelated files.** The repo doesn't enforce a formatter on touch — only on changed lines.
- **Don't push to `main`.** Even maintainers branch; `main` stays a clean history of merged PRs.

---

## Where decisions live

If you're about to make a design call that affects more than one area — a new dependency, a security trade-off, a directory layout change — read [`docs/decisions.md`](docs/decisions.md) first to see whether the question has been answered, and if not, propose the answer in your PR description. We'll fold the accepted answer back into `docs/decisions.md` as a new `D##` entry on merge.

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

For context, decisions `D24`–`D29` in [`docs/decisions.md`](docs/decisions.md) document the most recent security pass — that's the kind of audit + remediation flow we want for any new finding.

---

## A small note on tone

The project is open by design — that's literally what *vula* means in isiZulu. We try to keep PRs and issues short, specific, and friendly. If a review comment feels harsh, it's almost certainly tiredness rather than judgement; ask for clarification and we'll rephrase.

Welcome aboard.

---

## Frozen invariants

These are hard constraints. PRs that violate them will not be merged regardless of quality:

- **No CGO** in any OSS Go code. Pure Go only.
- **No .tsx** files. Frontend is JSX only (`*.jsx`).
- **No Google SSO / OAuth** login flows.
- **No Stripe billing** integration — billing lives in vulos-cloud only.
- **No Rust rewrites** — Go throughout (see docs/decisions.md / D-language).
- Features requiring closed-source cloud infrastructure belong in `vulos-cloud`, not here.
- No new external runtime dependencies without prior discussion in a GitHub issue.

## Code of Conduct

We follow the [Contributor Covenant v2.1](https://www.contributor-covenant.org/version/2/1/code_of_conduct/).

## Commit Message Style

[Conventional Commits](https://www.conventionalcommits.org/) format is welcome but not required. Examples:

```
feat(sandbox): restrict syscalls via seccomp
fix(firstboot): handle missing /etc/hostname
chore: bump Go 1.22 → 1.23
```

## Testing Expectations

Before opening a PR:

```bash
cd backend && go test ./... && go vet ./... && cd ..   # backend
npm run lint           # frontend
npm test               # Vitest — the frontend security contract (also runs in CI)
make smoke             # SMOKE-01 peering routes; if you touched firstboot/installer
```

There is no `go.mod` at the repository root — the Go module is `backend/`
(`module vulos/backend`). Go commands run from inside `backend/`, or use the
Makefile wrappers from the root: `make build`, `make test-local`, `make
test-dev`, `make smoke`, `make help`.

All existing tests must pass. Security-relevant changes must include tests.

## Licensing

Vulos is MIT-licensed. By submitting a PR you agree your contribution is released under the MIT License. No CLA is required. You retain copyright on your contributions.
