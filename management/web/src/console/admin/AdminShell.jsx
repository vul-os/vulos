/**
 * console/admin/AdminShell.jsx — shared chrome + gate states for the Operator
 * console pages. Matches the instrument-panel design (mono headings, token-only
 * colours, honest loading/empty/error states) used across the console.
 */

import { Section, Card, Button } from '../../ui/index.jsx'

/* Shared, token-only styles for the operator pages. Zero hex. */
export const ADMIN_STYLES = `
.op-header { display:flex; align-items:baseline; justify-content:space-between; gap:var(--sp-2); flex-wrap:wrap; margin-bottom:var(--sp-3); }
.op-title { font-family:var(--font-mono); font-size:clamp(1.125rem,2.2vw,1.375rem); font-weight:700; letter-spacing:-0.025em; color:var(--text-primary); }
.op-sub { font-family:var(--font-mono); font-size:var(--text-sm); color:var(--text-faint); }
.op-kicker { font-family:var(--font-mono); font-size:var(--text-xs); letter-spacing:0.12em; text-transform:uppercase; color:var(--accent); margin-bottom:6px; }

.op-statrow { display:grid; grid-template-columns:repeat(auto-fit,minmax(160px,1fr)); gap:var(--sp-2); margin-bottom:var(--sp-3); }

.op-table { width:100%; }
.op-thead, .op-trow { display:grid; gap:var(--sp-2); align-items:center; }
.op-thead { padding:0 var(--sp-3) var(--sp-1-5); font-family:var(--font-mono); font-size:var(--text-xs); font-weight:500; letter-spacing:0.08em; text-transform:uppercase; color:var(--text-ghost); }
.op-trow { width:100%; padding:var(--sp-2) var(--sp-3); min-height:52px; font-family:var(--font-mono); background:transparent; border:none; border-bottom:1px solid var(--border-subtle); text-align:left; }
button.op-trow { cursor:pointer; transition:background 120ms var(--ease); }
button.op-trow:hover { background:var(--bg-hover); }
button.op-trow:focus-visible { outline:none; box-shadow:inset var(--focus-ring); }
.op-trow:last-child { border-bottom:none; }
.op-cell { font-size:var(--text-sm); min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; color:var(--text-secondary); }
.op-cell.mono { font-variant-numeric:tabular-nums; }
.op-cell.dim { color:var(--text-tertiary); }

.op-filter { display:flex; align-items:center; gap:var(--sp-1-5); flex-wrap:wrap; margin-bottom:var(--sp-3); }
.op-filter-label { font-family:var(--font-mono); font-size:var(--text-xs); letter-spacing:0.06em; text-transform:uppercase; color:var(--text-ghost); }
.op-input { font-family:var(--font-mono); font-size:var(--text-sm); color:var(--text-primary); background:var(--bg-elevated); border:1px solid var(--border-strong); border-radius:var(--radius-sm); padding:8px 12px; min-height:40px; min-width:200px; }
.op-input:focus-visible { outline:none; box-shadow:var(--focus-ring); border-color:var(--accent); }

.op-empty { text-align:center; padding:var(--sp-6) var(--sp-3); }
.op-empty-title { font-family:var(--font-mono); font-size:var(--text-base); font-weight:600; color:var(--text-secondary); margin-bottom:var(--sp-1); }
.op-empty-sub { font-family:var(--font-mono); font-size:var(--text-sm); color:var(--text-faint); line-height:1.6; max-width:46ch; margin:0 auto; }

.op-kv { display:grid; grid-template-columns:180px 1fr; gap:var(--sp-1) var(--sp-2); align-items:baseline; }
.op-kv dt { font-family:var(--font-mono); font-size:var(--text-xs); letter-spacing:0.05em; text-transform:uppercase; color:var(--text-ghost); }
.op-kv dd { margin:0; font-family:var(--font-mono); font-size:var(--text-sm); color:var(--text-secondary); word-break:break-all; }

.op-skel { border-radius:var(--radius-lg); background:linear-gradient(90deg,var(--bg-surface) 0%,var(--bg-hover) 50%,var(--bg-surface) 100%); background-size:200% 100%; animation:opShimmer 1.4s ease-in-out infinite; height:52px; margin-bottom:2px; }
@keyframes opShimmer { from { background-position:200% 0; } to { background-position:-200% 0; } }
@media (prefers-reduced-motion: reduce) { .op-skel { animation:none; } }

@media (max-width:640px) { .op-kv { grid-template-columns:1fr; gap:2px; } }
`

/** OpHeader — the page kicker + title + subtitle. */
export function OpHeader({ title, sub }) {
  return (
    <div>
      <div className="op-kicker">Operator console</div>
      <div className="op-header">
        <span className="op-title">{title}</span>
        {sub && <span className="op-sub">{sub}</span>}
      </div>
    </div>
  )
}

/** OpGate — renders the correct honest state for a gated resource, or `children`
 * when authorised. Pass the flags straight from useAdminResource. */
export function OpGate({ loading, error, needsAdminSession, notOperator, onRetry, children }) {
  if (needsAdminSession) {
    return (
      <Card>
        <div className="op-empty">
          <div className="op-empty-title">Operator sign-in required</div>
          <div className="op-empty-sub">
            You&apos;re signed in, but the operator console needs a separate,
            hardware-backed admin session. Complete the operator login to continue.
          </div>
          <div style={{ marginTop: 'var(--sp-3)' }}>
            <Button href="/superadmin/login" variant="primary" size="sm">Operator sign-in →</Button>
          </div>
        </div>
      </Card>
    )
  }
  if (notOperator) {
    return (
      <Card>
        <div className="op-empty">
          <div className="op-empty-title">Operator access required</div>
          <div className="op-empty-sub">
            The operator console is available to platform admins on
            operator-enabled deployments only.
          </div>
        </div>
      </Card>
    )
  }
  if (loading) {
    return (
      <Card hover={false} style={{ padding: 'var(--sp-2)' }} role="status" aria-busy="true">
        {[0, 1, 2, 3, 4].map((i) => <div key={i} className="op-skel" aria-hidden="true" />)}
      </Card>
    )
  }
  if (error) {
    return (
      <Card>
        <div className="op-empty">
          <div className="op-empty-title">Couldn&apos;t load</div>
          <div className="op-empty-sub">Something went wrong reading this operator surface.</div>
          {onRetry && (
            <div style={{ marginTop: 'var(--sp-3)' }}>
              <Button onClick={onRetry} variant="ghost" size="sm">Try again</Button>
            </div>
          )}
        </div>
      </Card>
    )
  }
  return children
}

/** OpPage — Section wrapper that injects the shared styles once. */
export function OpPage({ children }) {
  return (
    <Section slim>
      <style>{ADMIN_STYLES}</style>
      {children}
    </Section>
  )
}
