# Contributing to Vulos Office

## Code of Conduct

We follow the [Contributor Covenant v2.1](https://www.contributor-covenant.org/version/2/1/code_of_conduct/).

## Dev Environment Setup

Requirements: Go 1.22+, Node 20+

```bash
# Backend
cd backend && go build ./...
go test ./...

# Frontend
npm install
npm run dev
```

For the PDF signing component, ensure `pdftk` or the equivalent pure-Go signing library is available (see `backend/signing/`).

## Branch and PR Conventions

- Branch off `main`. Name: `feat/description`, `fix/description`, `chore/description`.
- One logical change per PR. Keep diffs reviewable.
- PRs require at least one approving review.
- Squash-merge preferred.

## Commit Message Style

Conventional Commits welcome, not required:

```
feat(crdt): add undo/redo stack to fabric
fix(signing): handle multi-page PDF with form fields
chore: bump automerge to 2.x
```

## Testing Expectations

Before opening a PR:

```bash
go test ./...
go vet ./...
npm run lint
npm test
```

CRDT conflict-resolution logic is subtle — new CRDT paths require property-based or fuzz tests where feasible.

## Finding a Good First Issue

Look for `good first issue` or `help wanted` labels. UI polish, accessibility, and documentation are low-friction entry points.

## Scope: What We Say Yes and No To

### Yes
- Bug fixes and security improvements
- CRDT correctness and convergence improvements
- PDF signing robustness (edge-case documents)
- Spaces (collaboration) reliability and latency improvements
- Accessibility improvements
- Tests and documentation

### No — frozen invariants
- **No CGO** in any Go code. Pure Go only.
- **No .tsx** files. Frontend is JSX only (`*.jsx`).
- **No Google SSO / OAuth** login flows.
- **No Stripe billing** integration.
- **No Rust rewrites** — Go throughout.
- Live collaboration features that require vulos-cloud infrastructure belong there, not here.
- New runtime dependencies without prior issue discussion.

## Licensing

Vulos Office is MIT-licensed. Contributions inherit MIT. No CLA required.
