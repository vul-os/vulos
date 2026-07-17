import{t as e}from"./jsx-runtime-Qy3n81sD.js";import{i as t,n,t as r}from"./ui-J8a9L6bi.js";var i=e(),a=`
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
`;function o({title:e,sub:t}){return(0,i.jsxs)(`div`,{children:[(0,i.jsx)(`div`,{className:`op-kicker`,children:`Operator console`}),(0,i.jsxs)(`div`,{className:`op-header`,children:[(0,i.jsx)(`span`,{className:`op-title`,children:e}),t&&(0,i.jsx)(`span`,{className:`op-sub`,children:t})]})]})}function s({loading:e,error:t,needsAdminSession:a,notOperator:o,onRetry:s,children:c}){return a?(0,i.jsx)(n,{children:(0,i.jsxs)(`div`,{className:`op-empty`,children:[(0,i.jsx)(`div`,{className:`op-empty-title`,children:`Operator sign-in required`}),(0,i.jsx)(`div`,{className:`op-empty-sub`,children:`You're signed in, but the operator console needs a separate, hardware-backed admin session. Complete the operator login to continue.`}),(0,i.jsx)(`div`,{style:{marginTop:`var(--sp-3)`},children:(0,i.jsx)(r,{href:`/superadmin/login`,variant:`primary`,size:`sm`,children:`Operator sign-in →`})})]})}):o?(0,i.jsx)(n,{children:(0,i.jsxs)(`div`,{className:`op-empty`,children:[(0,i.jsx)(`div`,{className:`op-empty-title`,children:`Operator access required`}),(0,i.jsx)(`div`,{className:`op-empty-sub`,children:`The operator console is available to platform super-admins on operator-enabled deployments only.`})]})}):e?(0,i.jsx)(n,{hover:!1,style:{padding:`var(--sp-2)`},role:`status`,"aria-busy":`true`,children:[0,1,2,3,4].map(e=>(0,i.jsx)(`div`,{className:`op-skel`,"aria-hidden":`true`},e))}):t?(0,i.jsx)(n,{children:(0,i.jsxs)(`div`,{className:`op-empty`,children:[(0,i.jsx)(`div`,{className:`op-empty-title`,children:`Couldn't load`}),(0,i.jsx)(`div`,{className:`op-empty-sub`,children:`Something went wrong reading this operator surface.`}),s&&(0,i.jsx)(`div`,{style:{marginTop:`var(--sp-3)`},children:(0,i.jsx)(r,{onClick:s,variant:`ghost`,size:`sm`,children:`Try again`})})]})}):c}function c({children:e}){return(0,i.jsxs)(t,{slim:!0,children:[(0,i.jsx)(`style`,{children:a}),e]})}function l(e){if(!e)return`—`;let t=new Date(e.includes(`T`)?e:e.replace(` `,`T`)+`Z`);return isNaN(t.getTime())?e:t.toLocaleString(void 0,{dateStyle:`medium`,timeStyle:`short`})}function u(e){if(!e)return``;let t=new Date(e.includes(`T`)?e:e.replace(` `,`T`)+`Z`);if(isNaN(t.getTime()))return``;let n=Math.round((Date.now()-t.getTime())/1e3);if(n<0)return``;if(n<60)return`just now`;let r=Math.round(n/60);if(r<60)return`${r}m ago`;let i=Math.round(r/60);if(i<24)return`${i}h ago`;let a=Math.round(i/24);if(a<30)return`${a}d ago`;let o=Math.round(a/30);return o<12?`${o}mo ago`:`${Math.round(o/12)}y ago`}function d(e,t=10,n=6){return e?e.length<=t+n+1?e:`${e.slice(0,t)}…${e.slice(-n)}`:`—`}function f(e){let t=(e||``).toLowerCase();return t.includes(`denied`)||t.includes(`locked`)||t.includes(`suspend`)||t.includes(`delete`)||t.includes(`block`)?`danger`:t.includes(`reset`)||t.includes(`throttle`)||t.includes(`revoke`)||t.includes(`unsuspend`)?`warn`:t.includes(`login`)||t.includes(`request`)||t.includes(`create`)||t.includes(`add`)?`good`:`faint`}function p(e,t,n){return e?{tone:`danger`,label:t}:{tone:`good`,label:n}}export{d as a,c,u as i,p as n,s as o,l as r,o as s,f as t};