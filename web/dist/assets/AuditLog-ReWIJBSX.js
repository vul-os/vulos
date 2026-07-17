import{n as e,t}from"./jsx-runtime-Qy3n81sD.js";import{i as n,n as r,r as i,t as a}from"./ui-J8a9L6bi.js";var o=e();function s(e){if(!e||typeof e!=`string`)return`Unknown action`;let t=e.trim().replace(/[._-]+/g,` `).split(/\s+/).filter(Boolean);return t.length===0?`Unknown action`:t.map((e,t)=>t===0?e.charAt(0).toUpperCase()+e.slice(1):e).join(` `)}function c(e){let t=(e||``).toLowerCase();return/(remov|delete|destroy|revok|suspend|disabl|deactivat|ban|fail)/.test(t)?`danger`:/(add|creat|grant|invit|accept|enabl|verif|activat|restor|resum)/.test(t)?`good`:/(bill|invoice|payment|charge|topup|top_up|subscription|upgrad|downgrad)/.test(t)?`accent`:/(role|permission|owner|admin)/.test(t)?`warn`:`faint`}function l(e){if(!e)return`—`;try{let t=new Date(e);return Number.isNaN(t.getTime())?`—`:t.toLocaleString(void 0,{year:`numeric`,month:`short`,day:`numeric`,hour:`2-digit`,minute:`2-digit`})}catch{return`—`}}function u(e,t=Date.now()){if(!e)return``;let n=new Date(e).getTime();if(Number.isNaN(n))return``;let r=Math.max(0,Math.floor((t-n)/1e3));if(r<45)return`just now`;let i=Math.floor(r/60);if(i<60)return`${i}m ago`;let a=Math.floor(i/60);if(a<24)return`${a}h ago`;let o=Math.floor(a/24);if(o<30)return`${o}d ago`;let s=Math.floor(o/30);return s<12?`${s}mo ago`:`${Math.floor(s/12)}y ago`}function d(e,t=20){if(e==null||e===``)return`—`;let n=String(e);if(n.includes(`@`)||n.length<=t)return n;let r=Math.ceil((t-1)/2),i=Math.floor((t-1)/2);return`${n.slice(0,r)}…${n.slice(n.length-i)}`}function f(e){return!e||typeof e!=`object`||Array.isArray(e)?[]:Object.keys(e).sort().map(t=>({key:t,value:String(e[t])}))}function p(e,t){let n=Array.isArray(e)?e:[];return{hasPrev:n.length>0,hasNext:!!t,pageNumber:n.length+1,cursor:n.length>0?n[n.length-1]:``}}var m=t(),h=50;function g({action:e,cursor:t}){let[n,r]=(0,o.useState)(null),[i,a]=(0,o.useState)(!0),[s,c]=(0,o.useState)(null),[l,u]=(0,o.useState)(!1),d=(0,o.useCallback)(()=>{let n=!1;a(!0),c(null),u(!1);let i=new URLSearchParams({limit:String(h)});return e&&i.set(`action`,e),t&&i.set(`cursor`,t),fetch(`/api/org/audit?${i.toString()}`,{credentials:`include`,headers:{Accept:`application/json`}}).then(e=>{if(e.status===403)return n||(u(!0),a(!1)),null;if(!e.ok)throw Error(`HTTP ${e.status}`);return e.json()}).then(e=>{!n&&e&&(r(e),a(!1))}).catch(e=>{n||(c(e.message),a(!1))}),()=>{n=!0}},[e,t]);return(0,o.useEffect)(()=>d(),[d]),{data:n,loading:i,error:s,forbidden:l,reload:d}}function _({entry:e}){let[t,n]=(0,o.useState)(!1),r=c(e.action),a=f(e.metadata),p=a.length>0||!!e.target,h=`au-detail-${e.seq}`,g=s(e.action),_=u(e.ts),v=p?`button`:`div`,y=p?{onClick:()=>n(e=>!e),"aria-expanded":t,"aria-controls":h,"aria-label":`${g}${e.actor?` by ${e.actor}`:``} — ${l(e.ts)}. ${t?`Hide`:`Show`} details`}:{};return(0,m.jsxs)(`div`,{className:`au-row${t?` open`:``}`,children:[(0,m.jsxs)(v,{className:`au-row-main`,...y,children:[(0,m.jsx)(`span`,{className:`au-cell au-action`,children:(0,m.jsx)(i,{color:r===`faint`?`faint`:r,dot:!0,children:g})}),(0,m.jsx)(`span`,{className:`au-cell au-actor`,title:e.actor||void 0,children:d(e.actor)}),(0,m.jsx)(`span`,{className:`au-cell au-target`,title:e.target||void 0,children:d(e.target)}),(0,m.jsxs)(`span`,{className:`au-cell au-time`,children:[(0,m.jsx)(`time`,{dateTime:e.ts||void 0,title:l(e.ts),children:l(e.ts)}),_&&(0,m.jsx)(`span`,{className:`au-time-rel`,children:_})]}),p?(0,m.jsx)(`span`,{className:`au-chevron`,"aria-hidden":`true`,children:t?`▾`:`▸`}):(0,m.jsx)(`span`,{className:`au-chevron`,"aria-hidden":`true`})]}),t&&p&&(0,m.jsxs)(`div`,{className:`au-detail`,id:h,children:[e.target&&(0,m.jsxs)(`div`,{className:`au-detail-line`,children:[(0,m.jsx)(`span`,{className:`au-detail-key`,children:`Target`}),(0,m.jsx)(`span`,{className:`au-detail-val au-mono`,children:e.target})]}),a.map(e=>(0,m.jsxs)(`div`,{className:`au-detail-line`,children:[(0,m.jsx)(`span`,{className:`au-detail-key`,children:e.key}),(0,m.jsx)(`span`,{className:`au-detail-val au-mono`,children:e.value})]},e.key)),(0,m.jsxs)(`div`,{className:`au-detail-line`,children:[(0,m.jsx)(`span`,{className:`au-detail-key`,children:`Entry ID`}),(0,m.jsx)(`span`,{className:`au-detail-val au-mono`,children:e.id||`—`})]})]})]})}var v=`
.au-visually-hidden {
  position: absolute;
  width: 1px; height: 1px;
  padding: 0; margin: -1px;
  overflow: hidden; clip: rect(0 0 0 0);
  white-space: nowrap; border: 0;
}
.au-header {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--sp-2);
  flex-wrap: wrap;
  margin-bottom: var(--sp-3);
}
.au-header-title {
  font-family: var(--font-mono);
  font-size: clamp(1.125rem, 2.2vw, 1.375rem);
  font-weight: 700;
  letter-spacing: -0.025em;
  color: var(--text-primary);
}
.au-header-sub { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); }

/* Filter bar */
.au-filter {
  display: flex;
  align-items: center;
  gap: var(--sp-1-5);
  flex-wrap: wrap;
  margin-bottom: var(--sp-3);
}
.au-filter-label {
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--text-ghost);
}
.au-filter-input {
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text-primary);
  background: var(--bg-elevated);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-sm);
  padding: 8px 12px;
  min-height: 40px;
  min-width: 200px;
}
.au-filter-input:focus-visible { outline: none; box-shadow: var(--focus-ring); border-color: var(--accent); }

/* Table */
.au-table-head {
  display: grid;
  grid-template-columns: 190px 1fr 1fr 170px 24px;
  gap: var(--sp-2);
  padding: 0 var(--sp-3) var(--sp-1-5);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  font-weight: 500;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--text-ghost);
}
.au-row { border-bottom: 1px solid var(--border-subtle); }
.au-row:last-child { border-bottom: none; }
.au-row-main {
  display: grid;
  grid-template-columns: 190px 1fr 1fr 170px 24px;
  gap: var(--sp-2);
  align-items: center;
  width: 100%;
  padding: var(--sp-2) var(--sp-3);
  background: transparent;
  border: none;
  text-align: left;
  min-height: 52px;
  font-family: var(--font-mono);
}
button.au-row-main { cursor: pointer; transition: background 120ms var(--ease); }
button.au-row-main:hover { background: var(--bg-hover); }
button.au-row-main:focus-visible { outline: none; box-shadow: inset var(--focus-ring); }
.au-cell { font-size: var(--text-sm); min-width: 0; }
.au-actor { color: var(--text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.au-target { color: var(--text-tertiary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.au-time {
  display: flex; flex-direction: column; gap: 1px;
  color: var(--text-tertiary);
  font-variant-numeric: tabular-nums;
  text-align: right;
}
.au-time-rel { font-size: var(--text-xs); color: var(--text-ghost); }
.au-chevron { color: var(--text-ghost); font-size: var(--text-xs); text-align: center; }

.au-detail {
  padding: var(--sp-2) var(--sp-3) var(--sp-3);
  background: var(--bg-surface);
  display: flex;
  flex-direction: column;
  gap: var(--sp-1);
}
.au-detail-line {
  display: grid;
  grid-template-columns: 140px 1fr;
  gap: var(--sp-2);
  align-items: baseline;
}
.au-detail-key {
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--text-ghost);
}
.au-detail-val {
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text-secondary);
}
.au-mono { word-break: break-all; }

/* Pager */
.au-pager {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--sp-2);
  margin-top: var(--sp-3);
  flex-wrap: wrap;
}
.au-pager-info { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-faint); }
.au-pager-btns { display: flex; gap: var(--sp-1-5); }

/* States */
.au-empty { text-align: center; padding: var(--sp-6) var(--sp-3); }
.au-empty-icon { color: var(--text-ghost); margin-bottom: var(--sp-2); display: flex; justify-content: center; }
.au-empty-title {
  font-family: var(--font-mono);
  font-size: var(--text-base);
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: var(--sp-1);
}
.au-empty-sub {
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  color: var(--text-faint);
  line-height: 1.6;
  max-width: 46ch;
  margin: 0 auto;
}
.au-state-error {
  padding: var(--sp-3);
  border: 1px solid color-mix(in srgb, var(--danger) 30%, transparent);
  border-radius: var(--radius-lg);
  background: color-mix(in srgb, var(--danger) 6%, transparent);
  color: var(--text-secondary);
  display: flex; align-items: center; justify-content: space-between;
  gap: var(--sp-2); flex-wrap: wrap;
  font-family: var(--font-mono); font-size: var(--text-sm);
  margin-bottom: var(--sp-3);
}
.au-retry-btn {
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  font-weight: 500;
  color: var(--text-primary);
  background: var(--bg-elevated);
  border: 1px solid var(--border-emphasis);
  border-radius: var(--radius-sm);
  padding: 8px 14px;
  cursor: pointer;
  min-height: 44px;
  transition: border-color 120ms var(--ease), background 120ms var(--ease);
}
.au-retry-btn:hover { border-color: var(--accent); background: var(--bg-hover); }
.au-retry-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); border-color: var(--accent); }
.au-skel {
  border-radius: var(--radius-lg);
  background: linear-gradient(90deg, var(--bg-surface) 0%, var(--bg-hover) 50%, var(--bg-surface) 100%);
  background-size: 200% 100%;
  animation: auShimmer 1.4s ease-in-out infinite;
  height: 52px;
  margin-bottom: 2px;
}
@keyframes auShimmer { from { background-position: 200% 0; } to { background-position: -200% 0; } }
@media (prefers-reduced-motion: reduce) { .au-skel { animation: none; } }

/* Responsive: drop the target column, then the actor column, on narrow viewports */
@media (max-width: 860px) {
  .au-table-head { grid-template-columns: 160px 1fr 150px 20px; }
  .au-row-main   { grid-template-columns: 160px 1fr 150px 20px; }
  .au-table-head .au-target, .au-row-main .au-target { display: none; }
}
@media (max-width: 560px) {
  .au-table-head { grid-template-columns: 1fr 120px 20px; }
  .au-row-main   { grid-template-columns: 1fr 120px 20px; }
  .au-table-head .au-actor, .au-row-main .au-actor { display: none; }
  .au-detail-line { grid-template-columns: 1fr; gap: 2px; }
}
`;function y(){let[e,t]=(0,o.useState)(``),[i,s]=(0,o.useState)(``),[c,l]=(0,o.useState)([]),{data:u,loading:d,error:f,forbidden:h,reload:y}=g({action:i,cursor:p(c,``).cursor});(0,o.useEffect)(()=>{let t=setTimeout(()=>{s(e.trim()),l([])},350);return()=>clearTimeout(t)},[e]);let b=u?.entries??[],x=u?.next_cursor??``,{hasPrev:S,hasNext:C,pageNumber:w}=p(c,x);return(0,m.jsxs)(n,{slim:!0,children:[(0,m.jsx)(`style`,{children:v}),(0,m.jsxs)(`div`,{className:`au-header`,children:[(0,m.jsx)(`span`,{className:`au-header-title`,children:`Audit log`}),(0,m.jsx)(`span`,{className:`au-header-sub`,children:b.length>0?`${b.length} event${b.length===1?``:`s`}${S?` (older page)`:``}`:`Who did what in your organisation`})]}),!h&&(0,m.jsxs)(`div`,{className:`au-filter`,children:[(0,m.jsx)(`label`,{className:`au-filter-label`,htmlFor:`au-action-filter`,children:`Filter by action`}),(0,m.jsx)(`input`,{id:`au-action-filter`,className:`au-filter-input`,type:`text`,value:e,onChange:e=>t(e.target.value),placeholder:`e.g. member_added, role_changed`,autoComplete:`off`,spellCheck:!1,"aria-label":`Filter audit events by action label`})]}),h&&(0,m.jsx)(r,{children:(0,m.jsxs)(`div`,{className:`au-empty`,children:[(0,m.jsx)(`div`,{className:`au-empty-icon`,"aria-hidden":`true`,children:(0,m.jsxs)(`svg`,{width:`40`,height:`40`,viewBox:`0 0 24 24`,fill:`none`,stroke:`currentColor`,strokeWidth:`1.3`,strokeLinecap:`round`,strokeLinejoin:`round`,children:[(0,m.jsx)(`rect`,{x:`4`,y:`10`,width:`16`,height:`10`,rx:`2`}),(0,m.jsx)(`path`,{d:`M8 10V7a4 4 0 0 1 8 0v3`})]})}),(0,m.jsx)(`div`,{className:`au-empty-title`,children:`Admins only`}),(0,m.jsx)(`div`,{className:`au-empty-sub`,children:`The organisation audit trail is available to owners and admins. Ask an admin for access if you need to review account activity.`})]})}),f&&!d&&!h&&(0,m.jsxs)(`div`,{className:`au-state-error`,role:`alert`,children:[(0,m.jsx)(`span`,{children:`Couldn't load the audit log right now.`}),(0,m.jsx)(`button`,{className:`au-retry-btn`,onClick:()=>y(),"aria-label":`Retry loading the audit log`,children:`Try again`})]}),d&&!h&&(0,m.jsxs)(r,{hover:!1,style:{padding:`var(--sp-2)`},role:`status`,"aria-live":`polite`,"aria-busy":`true`,children:[(0,m.jsx)(`span`,{className:`au-visually-hidden`,children:`Loading the audit log…`}),[0,1,2,3,4,5].map(e=>(0,m.jsx)(`div`,{className:`au-skel`,"aria-hidden":`true`},e))]}),!d&&!f&&!h&&b.length===0&&(0,m.jsx)(r,{children:(0,m.jsxs)(`div`,{className:`au-empty`,children:[(0,m.jsx)(`div`,{className:`au-empty-icon`,"aria-hidden":`true`,children:(0,m.jsx)(`svg`,{width:`40`,height:`40`,viewBox:`0 0 24 24`,fill:`none`,stroke:`currentColor`,strokeWidth:`1.3`,strokeLinecap:`round`,strokeLinejoin:`round`,children:(0,m.jsx)(`path`,{d:`M4 5h16M4 12h16M4 19h10`})})}),(0,m.jsx)(`div`,{className:`au-empty-title`,children:i?`No matching events`:`No activity recorded yet`}),(0,m.jsx)(`div`,{className:`au-empty-sub`,children:i?`Nothing matches “${i}”. Clear the filter to see all events.`:`Member changes, role updates, invites and billing events will appear here as they happen.`})]})}),!d&&!f&&!h&&b.length>0&&(0,m.jsxs)(m.Fragment,{children:[(0,m.jsxs)(r,{hover:!1,style:{padding:0,overflow:`hidden`},children:[(0,m.jsxs)(`div`,{className:`au-table-head`,"aria-hidden":`true`,children:[(0,m.jsx)(`span`,{className:`au-action`,children:`Action`}),(0,m.jsx)(`span`,{className:`au-actor`,children:`Actor`}),(0,m.jsx)(`span`,{className:`au-target`,children:`Target`}),(0,m.jsx)(`span`,{className:`au-time`,children:`When`}),(0,m.jsx)(`span`,{})]}),b.map(e=>(0,m.jsx)(_,{entry:e},e.seq??e.id))]}),(S||C)&&(0,m.jsxs)(`div`,{className:`au-pager`,children:[(0,m.jsx)(`span`,{className:`au-pager-info`,children:S?`Page ${w}`:`Newest events`}),(0,m.jsxs)(`div`,{className:`au-pager-btns`,children:[(0,m.jsx)(a,{variant:`ghost`,size:`sm`,onClick:()=>l(e=>e.slice(0,-1)),disabled:!S,"aria-label":`Newer events`,children:`← Newer`}),(0,m.jsx)(a,{variant:`ghost`,size:`sm`,onClick:()=>{x&&l(e=>[...e,x])},disabled:!C,"aria-label":`Older events`,children:`Older →`})]})]})]})]})}export{y as default};