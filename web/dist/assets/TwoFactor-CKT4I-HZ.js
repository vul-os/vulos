import{n as e,t}from"./jsx-runtime-Qy3n81sD.js";import{d as n,l as r,u as i}from"./index-D1tNtzbM.js";import{i as a}from"./ui-CTsMyeue.js";import{AuthFormStyles as o}from"./Login-C_O75i4O.js";var s=e(),c=t(),l=`/api/auth/totp/verify`;function u(){try{let e=new URL(window.location.href).searchParams.get(`return`);if(e&&e.startsWith(`/`)&&!e.startsWith(`//`))return e}catch{}return`/`}function d({size:e=14}){return(0,c.jsx)(`span`,{"aria-hidden":`true`,style:{display:`inline-block`,width:e,height:e,borderRadius:`50%`,border:`2px solid color-mix(in srgb, currentColor 25%, transparent)`,borderTopColor:`currentColor`,animation:`vcAuthSpin 0.7s linear infinite`}})}function f({retryAfter:e,onExpired:t}){let[n,r]=(0,s.useState)(e),i=(0,s.useRef)(null),a=(0,s.useRef)(t);(0,s.useEffect)(()=>{a.current=t}),(0,s.useEffect)(()=>{if(e<=0){a.current();return}return i.current=setInterval(()=>{r(e=>e<=1?(clearInterval(i.current),a.current(),0):e-1)},1e3),()=>clearInterval(i.current)},[]);let o=String(Math.floor(n/60)).padStart(2,`0`),l=String(n%60).padStart(2,`0`);return(0,c.jsx)(`div`,{role:`alert`,"aria-live":`assertive`,className:`vc-auth-alert`,style:{flexDirection:`column`,gap:8},children:(0,c.jsxs)(`div`,{style:{display:`flex`,alignItems:`flex-start`,gap:10},children:[(0,c.jsx)(`span`,{className:`vc-auth-alert-tag`,children:`Locked`}),(0,c.jsxs)(`span`,{className:`vc-auth-alert-msg`,children:[`Too many failed attempts. Try again in`,` `,(0,c.jsxs)(`span`,{style:{fontFamily:`var(--font-mono)`,fontWeight:600,color:`var(--warn)`},"aria-live":`off`,children:[o,`:`,l]}),`.`]})]})})}function p(){let{user:e,loading:t,refresh:p}=i(),[m,h]=(0,s.useState)(!1),[g,_]=(0,s.useState)(``),[v,y]=(0,s.useState)(``),[b,x]=(0,s.useState)(!1),[S,C]=(0,s.useState)(!1),[w,T]=(0,s.useState)(null),[E,D]=(0,s.useState)(null),O=(0,s.useMemo)(()=>u(),[]);(0,s.useEffect)(()=>{!t&&e&&n(O)},[t,e,O]);let k=(m?v:g).trim().length>0&&!S&&E===null;function A(e){if(w&&T(null),m)y(e.target.value);else{let t=e.target.value.replace(/\D/g,``).slice(0,6);_(t)}}function j(e){e.preventDefault(),h(e=>!e),_(``),y(``),T(null)}async function M(e){if(e.preventDefault(),k){T(null),C(!0);try{let e={remember_device:b,...m?{recovery_code:v.trim()}:{code:g.trim()}},t=await fetch(l,{method:`POST`,credentials:`include`,headers:{"Content-Type":`application/json`,Accept:`application/json`},body:JSON.stringify(e)});if(t.status===429){let e=60;try{let n=await t.json();typeof n.retry_after==`number`&&n.retry_after>0&&(e=n.retry_after)}catch{}D(e),C(!1);return}if(!t.ok){let e=`Invalid code. Please try again.`;try{let n=await t.json();n&&typeof n.error==`string`&&n.error&&(e=n.error)}catch{}T(e),C(!1);return}await p(),n(O)}catch{T(`Network error. Please check your connection and try again.`),C(!1)}}}return(0,c.jsxs)(a,{slim:!0,children:[(0,c.jsx)(o,{}),(0,c.jsx)(`style`,{children:`
        .vc-2fa-digits {
          letter-spacing: 0.35em;
          text-align: center;
          font-size: 1.5rem;
          font-family: var(--font-mono);
          font-weight: 600;
        }
        .vc-2fa-digits::placeholder {
          letter-spacing: 0.1em;
          font-size: 0.9375rem;
          font-weight: 400;
        }
        .vc-auth-check-row {
          display: flex;
          align-items: center;
          gap: 10px;
          cursor: pointer;
          user-select: none;
        }
        .vc-auth-check-row input[type=checkbox] {
          width: 16px;
          height: 16px;
          accent-color: var(--accent);
          cursor: pointer;
          flex-shrink: 0;
        }
        .vc-auth-check-label {
          font-family: var(--font-mono);
          font-size: 0.8125rem;
          color: var(--text-secondary);
          line-height: 1.4;
        }
        .vc-auth-mode-toggle {
          background: none;
          border: none;
          padding: 0;
          font-family: var(--font-mono);
          font-size: 0.8125rem;
          color: var(--accent);
          cursor: pointer;
          text-decoration: underline;
          text-underline-offset: 2px;
          text-decoration-color: color-mix(in srgb, var(--accent) 40%, transparent);
          transition: color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
        }
        .vc-auth-mode-toggle:hover {
          color: var(--accent-hover);
          text-decoration-color: var(--accent-hover);
        }
        .vc-auth-mode-toggle:focus-visible {
          outline: none;
          box-shadow: var(--focus-ring);
          border-radius: var(--radius-sm, 6px);
        }
      `}),(0,c.jsx)(`div`,{className:`vc-auth-wrap`,children:(0,c.jsxs)(`div`,{className:`vc-auth-card vc-auth-reveal`,children:[(0,c.jsxs)(`div`,{className:`vc-auth-head`,children:[(0,c.jsxs)(`a`,{href:`/`,onClick:e=>{e.preventDefault(),n(`/`)},className:`vc-auth-brand`,"aria-label":`Vulos — home`,children:[(0,c.jsx)(r,{size:36,tone:`on-dark`}),(0,c.jsxs)(`span`,{className:`vc-auth-wordmark`,children:[`vulos`,(0,c.jsx)(`span`,{className:`vc-auth-wordmark-suffix`,children:`.org`})]})]}),(0,c.jsx)(`p`,{className:`vc-auth-eyebrow`,children:`Two-Factor Authentication`}),(0,c.jsx)(`h1`,{className:`vc-auth-title`,children:`Verify your identity`}),(0,c.jsx)(`p`,{className:`vc-auth-subtitle`,children:m?`Enter one of your saved recovery codes to regain access.`:`Enter the 6-digit code from your authenticator app.`})]}),(0,c.jsxs)(`form`,{noValidate:!0,onSubmit:M,className:`vc-auth-form`,children:[E!==null&&(0,c.jsx)(f,{retryAfter:E,onExpired:()=>D(null)},E),w&&E===null&&(0,c.jsxs)(`div`,{role:`alert`,id:`tfa-error`,className:`vc-auth-alert`,"aria-live":`assertive`,children:[(0,c.jsx)(`span`,{className:`vc-auth-alert-tag`,children:`Error`}),(0,c.jsx)(`span`,{className:`vc-auth-alert-msg`,children:w})]}),(0,c.jsxs)(`div`,{className:`vc-auth-field`,children:[(0,c.jsx)(`div`,{className:`vc-auth-label-row`,children:(0,c.jsx)(`label`,{htmlFor:`tfa-code`,className:`vc-auth-label`,children:m?`Recovery code`:`Authentication code`})}),m?(0,c.jsx)(`input`,{id:`tfa-code`,name:`recovery_code`,type:`text`,autoComplete:`one-time-code`,spellCheck:!1,autoCapitalize:`characters`,value:v,onChange:A,placeholder:`XXXX-XXXX-XXXX`,required:!0,disabled:E!==null||S,"aria-invalid":w?`true`:`false`,"aria-describedby":w?`tfa-error`:void 0,className:`vc-auth-input${w?` has-error`:``}`,autoFocus:!0}):(0,c.jsx)(`input`,{id:`tfa-code`,name:`code`,type:`text`,inputMode:`numeric`,autoComplete:`one-time-code`,pattern:`[0-9]*`,maxLength:6,spellCheck:!1,value:g,onChange:A,placeholder:`000000`,required:!0,disabled:E!==null||S,"aria-invalid":w?`true`:`false`,"aria-describedby":w?`tfa-error`:void 0,className:`vc-auth-input vc-2fa-digits${w?` has-error`:``}`,autoFocus:!0})]}),(0,c.jsxs)(`label`,{className:`vc-auth-check-row`,children:[(0,c.jsx)(`input`,{type:`checkbox`,checked:b,onChange:e=>x(e.target.checked),disabled:E!==null||S,"aria-label":`Trust this device for 30 days`}),(0,c.jsx)(`span`,{className:`vc-auth-check-label`,children:`Remember this device for 30 days`})]}),(0,c.jsx)(`button`,{type:`submit`,className:`vc-auth-submit`,disabled:!k,"aria-busy":S?`true`:`false`,children:S?(0,c.jsxs)(c.Fragment,{children:[(0,c.jsx)(d,{size:14}),(0,c.jsx)(`span`,{children:`Verifying…`})]}):`Verify`}),(0,c.jsx)(`div`,{className:`vc-auth-foot`,children:(0,c.jsxs)(`span`,{className:`vc-auth-foot-text`,children:[m?`Have your authenticator? `:`Lost access to your app? `,(0,c.jsx)(`button`,{type:`button`,className:`vc-auth-mode-toggle`,onClick:j,children:m?`Use authenticator code`:`Use a recovery code`})]})})]})]})})]})}export{p as default};