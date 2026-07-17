import{n as e,t}from"./jsx-runtime-Qy3n81sD.js";import{m as n}from"./index-ClKpOLvf.js";import{i as r,r as i}from"./ui-J8a9L6bi.js";import{a,i as o,n as s,r as c,t as l}from"./status-format-DyB4Ihqz.js";var u=e(),d=t();function f(e){let[t,n]=(0,u.useState)(null),[r,i]=(0,u.useState)(!0),[a,o]=(0,u.useState)(null),s=(0,u.useCallback)(()=>{i(!0),o(null),fetch(`/api/account/status`,{credentials:`include`,headers:{Accept:`application/json`}}).then(e=>{if(!e.ok)throw Error(`HTTP ${e.status}`);return e.json()}).then(e=>{n(e),i(!1)}).catch(e=>{o(e.message),i(!1)})},[]);return(0,u.useEffect)(()=>{s()},[s,e]),{data:t,loading:r,error:a,reload:s}}function p({overall:e,loading:t}){return t?(0,d.jsxs)(`div`,{className:`acs-banner`,"aria-live":`polite`,children:[(0,d.jsx)(`span`,{className:`acs-banner-dot`,style:{background:`var(--text-ghost)`},"aria-hidden":`true`}),(0,d.jsx)(`span`,{children:`Checking your services…`})]}):(0,d.jsxs)(`div`,{className:`acs-banner`,"data-state":e,role:`status`,children:[(0,d.jsx)(`span`,{className:`acs-banner-dot`,style:{background:a(e).tone},"aria-hidden":`true`}),(0,d.jsx)(`span`,{children:s(e).replace(`cloud services`,`services`)})]})}function m({box:e}){let t=a(e.health);return(0,d.jsxs)(`div`,{className:`acs-row`,children:[(0,d.jsxs)(`div`,{className:`acs-row-main`,children:[(0,d.jsx)(`span`,{className:`acs-dot`,style:{background:e.reachable?`var(--good)`:`var(--warn)`},"aria-hidden":`true`}),(0,d.jsxs)(`div`,{className:`acs-row-text`,children:[(0,d.jsx)(`span`,{className:`acs-row-name`,children:e.name||e.ulid||`Box`}),e.ulid&&(0,d.jsx)(`span`,{className:`acs-row-sub`,children:e.ulid})]})]}),(0,d.jsxs)(`div`,{className:`acs-row-right`,children:[(0,d.jsx)(`span`,{className:`acs-row-when`,children:e.last_seen?`seen ${c(e.last_seen)}`:`never seen`}),(0,d.jsx)(i,{color:t.pill,dot:!0,children:e.reachable?`reachable`:`unreachable`})]})]})}function h(){let[e,t]=(0,u.useState)(0),{data:s,loading:h,error:_,reload:v}=f(e);function y(){t(e=>e+1),v()}let b=s?.boxes??[],x=s?.services??[],S=s?.events??[],C=s?.relay??{},w=o(C.health);return(0,d.jsxs)(r,{slim:!0,children:[(0,d.jsx)(`style`,{children:g}),(0,d.jsxs)(`div`,{className:`acs-head`,children:[(0,d.jsxs)(`div`,{children:[(0,d.jsx)(`h1`,{className:`acs-title`,children:`Account status`}),(0,d.jsx)(`p`,{className:`acs-sub`,children:`The health of the boxes and services on your account.`})]}),(0,d.jsxs)(`button`,{className:`acs-reload`,onClick:y,disabled:h,"aria-busy":h,children:[`↺ `,h?`Refreshing…`:`Refresh`]})]}),_&&(0,d.jsxs)(`div`,{className:`acs-error`,role:`alert`,children:[`Could not load account status: `,_]}),!_&&(0,d.jsx)(p,{overall:s?.overall,loading:h}),!_&&!h&&(0,d.jsxs)(d.Fragment,{children:[(0,d.jsx)(`h2`,{className:`acs-h2`,children:`Your boxes`}),b.length===0?(0,d.jsxs)(`div`,{className:`acs-empty`,children:[`No boxes on this account yet.`,` `,(0,d.jsx)(`a`,{href:n(`/enroll`),"data-router":!0,children:`Enroll a box →`})]}):(0,d.jsx)(`div`,{className:`acs-list`,role:`list`,children:b.map(e=>(0,d.jsx)(`div`,{role:`listitem`,children:(0,d.jsx)(m,{box:e})},e.ulid||e.name))}),(0,d.jsx)(`h2`,{className:`acs-h2`,children:`Relay`}),(0,d.jsxs)(`div`,{className:`acs-relay`,children:[(0,d.jsxs)(`div`,{className:`acs-relay-cell`,children:[(0,d.jsx)(`span`,{className:`acs-cell-label`,children:`Fabric health`}),(0,d.jsx)(`span`,{className:`acs-cell-value`,children:(0,d.jsx)(i,{color:a(w).pill,dot:!0,children:C.health||`unavailable`})})]}),(0,d.jsxs)(`div`,{className:`acs-relay-cell`,children:[(0,d.jsx)(`span`,{className:`acs-cell-label`,children:`Relayed this month`}),(0,d.jsx)(`span`,{className:`acs-cell-value acs-num`,children:l(C.bytes??0)})]}),(0,d.jsxs)(`div`,{className:`acs-relay-cell`,children:[(0,d.jsx)(`span`,{className:`acs-cell-label`,children:`Sessions`}),(0,d.jsx)(`span`,{className:`acs-cell-value acs-num`,children:C.sessions??0})]}),(0,d.jsxs)(`div`,{className:`acs-relay-cell`,children:[(0,d.jsx)(`span`,{className:`acs-cell-label`,children:`Period`}),(0,d.jsx)(`span`,{className:`acs-cell-value acs-num`,children:C.period||`—`})]})]}),(0,d.jsx)(`h2`,{className:`acs-h2`,children:`Provisioned services`}),x.length===0?(0,d.jsx)(`div`,{className:`acs-empty`,children:`No provisioned services.`}):(0,d.jsx)(`div`,{className:`acs-list`,role:`list`,children:x.map((e,t)=>{let n=a(e.status);return(0,d.jsxs)(`div`,{className:`acs-row`,role:`listitem`,children:[(0,d.jsxs)(`div`,{className:`acs-row-main`,children:[(0,d.jsx)(`span`,{className:`acs-dot`,style:{background:n.tone},"aria-hidden":`true`}),(0,d.jsx)(`span`,{className:`acs-row-name`,children:e.name})]}),(0,d.jsx)(i,{color:n.pill,dot:!0,children:n.label})]},`${e.name}-${t}`)})}),(0,d.jsx)(`h2`,{className:`acs-h2`,children:`Recent events`}),S.length===0?(0,d.jsx)(`div`,{className:`acs-empty`,children:`No recent events.`}):(0,d.jsx)(`ol`,{className:`acs-events`,role:`list`,children:S.map((e,t)=>(0,d.jsxs)(`li`,{children:[(0,d.jsx)(`time`,{dateTime:e.at,children:c(e.at)}),(0,d.jsx)(`span`,{className:`acs-event-kind`,children:e.kind}),(0,d.jsx)(`span`,{children:e.text})]},t))})]})]})}var g=`
  .acs-head { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--sp-2); flex-wrap: wrap; margin-bottom: var(--sp-4); }
  .acs-title { font-family: var(--font-mono); font-size: clamp(1rem, 2vw, 1.25rem); font-weight: 700; letter-spacing: -0.02em; margin: 0; }
  .acs-sub { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); margin-top: var(--sp-0-5); }
  .acs-reload {
    font-family: var(--font-mono); font-size: var(--text-xs); color: var(--accent);
    background: transparent; border: 1px solid var(--border-strong); border-radius: var(--radius-sm);
    padding: 8px 12px; min-height: 36px; cursor: pointer; flex-shrink: 0;
    transition: border-color 120ms var(--ease);
  }
  .acs-reload:hover { border-color: var(--border-emphasis); }
  .acs-reload:focus-visible { outline: none; box-shadow: var(--focus-ring); }

  .acs-banner {
    display: flex; align-items: center; gap: var(--sp-1-5);
    padding: var(--sp-2) var(--sp-3); border: 1px solid var(--border-strong);
    border-radius: var(--radius-lg); margin-bottom: var(--sp-4);
    font-family: var(--font-mono); font-size: var(--text-sm); font-weight: 500; color: var(--text-primary);
  }
  .acs-banner-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
  .acs-banner[data-state="operational"] { border-color: color-mix(in srgb, var(--good) 30%, var(--border-strong)); }
  .acs-banner[data-state="degraded"]    { border-color: color-mix(in srgb, var(--warn) 35%, var(--border-strong)); }
  .acs-banner[data-state="down"]        { border-color: color-mix(in srgb, var(--danger) 35%, var(--border-strong)); }

  .acs-h2 { font-family: var(--font-mono); font-size: var(--text-xs); font-weight: 500; letter-spacing: 0.09em; text-transform: uppercase; color: var(--text-ghost); margin: var(--sp-5) 0 var(--sp-1-5); }

  .acs-list { border: 1px solid var(--border-strong); border-radius: var(--radius-lg); overflow: hidden; background: var(--bg-surface); }
  .acs-row {
    display: flex; align-items: center; justify-content: space-between; gap: var(--sp-2);
    padding: var(--sp-2) var(--sp-2-5); border-bottom: 1px solid var(--border-strong); min-height: 52px;
  }
  .acs-list > *:last-child .acs-row, .acs-row:last-child { border-bottom: none; }
  .acs-row-main { display: flex; align-items: center; gap: var(--sp-1-5); min-width: 0; flex: 1; }
  .acs-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
  .acs-row-text { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
  .acs-row-name { font-family: var(--font-mono); font-size: var(--text-sm); font-weight: 600; color: var(--text-primary); }
  .acs-row-sub { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-ghost); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .acs-row-right { display: flex; align-items: center; gap: var(--sp-1-5); flex-shrink: 0; }
  .acs-row-when { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-faint); white-space: nowrap; }

  .acs-relay { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: var(--sp-2); }
  .acs-relay-cell {
    display: flex; flex-direction: column; gap: 4px;
    padding: var(--sp-2) var(--sp-2-5); border: 1px solid var(--border-strong);
    border-radius: var(--radius-lg); background: var(--bg-surface);
  }
  .acs-cell-label { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-ghost); text-transform: uppercase; letter-spacing: 0.06em; }
  .acs-cell-value { font-size: var(--text-sm); color: var(--text-primary); }
  .acs-num { font-family: var(--font-mono); font-weight: 600; font-variant-numeric: tabular-nums; }

  .acs-events { list-style: none; margin: 0; padding: 0; display: grid; gap: var(--sp-1); }
  .acs-events li {
    display: grid; grid-template-columns: 90px 90px 1fr; gap: var(--sp-1-5); align-items: baseline;
    padding: var(--sp-1) var(--sp-2); border: 1px solid var(--border-strong); border-radius: var(--radius);
    background: var(--bg-surface); font-size: var(--text-sm); color: var(--text-secondary);
  }
  .acs-events time { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-faint); }
  .acs-event-kind { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--accent); }

  .acs-empty { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); padding: var(--sp-3); border: 1px dashed var(--border-strong); border-radius: var(--radius-lg); }
  .acs-empty a { color: var(--accent); text-decoration: none; }
  .acs-error { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--danger); padding: var(--sp-2) var(--sp-3); border: 1px solid color-mix(in srgb, var(--danger) 30%, var(--border-strong)); border-radius: var(--radius-lg); margin-bottom: var(--sp-3); }

  @media (max-width: 480px) {
    .acs-events li { grid-template-columns: 1fr; gap: 2px; }
    .acs-row-when { display: none; }
  }
`;export{h as default};