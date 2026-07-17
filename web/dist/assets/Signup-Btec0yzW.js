import{l as e,n as t,o as n,r,t as i}from"./index-CJMTmyIi.js";import{t as a}from"./ui-BdNV8GBy.js";import{t as o}from"./Logo-CmDzBuLJ.js";import{AuthFormStyles as s,n as c,t as l}from"./Login-B-l_Yn0r.js";var u=e(),d=t(),f=12,p=/^[^\s@]+@[^\s@]+\.[^\s@]+$/;function m(){try{let e=new URL(window.location.href).searchParams.get(`return`);if(e&&e.startsWith(`/`)&&!e.startsWith(`//`))return e}catch{}return null}async function h(e){n(e||await c())}function g(e){if(!e)return 0;let t=0;return e.length>=8&&(t+=1),e.length>=12&&(t+=1),/[A-Z]/.test(e)&&/[a-z]/.test(e)&&(t+=1),/\d/.test(e)&&/[^A-Za-z0-9]/.test(e)&&(t+=1),e.length>=16&&(t=Math.max(t,4)),Math.min(t,4)}var _=[``,`Weak`,`Fair`,`Good`,`Strong`],v=[``,`weak`,`okay`,`good`,`strong`];async function y(e){try{return await navigator.clipboard.writeText(e),!0}catch{return!1}}function b(e,t){let n=new Blob([t],{type:`text/plain`}),r=URL.createObjectURL(n),i=Object.assign(document.createElement(`a`),{href:r,download:e});document.body.appendChild(i),i.click(),document.body.removeChild(i),URL.revokeObjectURL(r)}function x({size:e=14}){return(0,d.jsx)(`span`,{"aria-hidden":`true`,style:{display:`inline-block`,width:e,height:e,borderRadius:`50%`,border:`2px solid color-mix(in srgb, currentColor 25%, transparent)`,borderTopColor:`currentColor`,animation:`vcAuthSpin 0.7s linear infinite`}})}function S({text:e,label:t=`Copy`}){let[n,r]=(0,u.useState)(!1);return(0,d.jsx)(`button`,{type:`button`,className:`su-btn su-btn--ghost su-btn--sm`,onClick:(0,u.useCallback)(async()=>{await y(e)&&(r(!0),setTimeout(()=>r(!1),2e3))},[e]),children:n?`Copied!`:t})}function C({codes:e}){let t=e.join(`
`);return(0,d.jsxs)(`div`,{style:{display:`flex`,flexDirection:`column`,gap:10},children:[(0,d.jsxs)(`p`,{className:`su-body`,style:{marginBottom:4},children:[(0,d.jsx)(`strong`,{style:{color:`var(--warn)`},children:`Save these recovery codes.`}),` `,`They will not be shown again.`]}),(0,d.jsx)(`div`,{className:`su-code-box`,children:(0,d.jsx)(`div`,{className:`su-codes-grid`,children:e.map((e,t)=>(0,d.jsx)(`span`,{className:`su-code`,children:e},t))})}),(0,d.jsxs)(`div`,{style:{display:`flex`,gap:8,flexWrap:`wrap`},children:[(0,d.jsx)(S,{text:t,label:`Copy all codes`}),(0,d.jsx)(`button`,{type:`button`,className:`su-btn su-btn--ghost su-btn--sm`,onClick:()=>b(`vulos-recovery-codes.txt`,t),children:`Download .txt`})]})]})}function w({step:e,total:t}){return(0,d.jsx)(`div`,{className:`su-progress-wrap`,"aria-label":`Step ${e} of ${t}`,children:Array.from({length:t},(t,n)=>(0,d.jsx)(`span`,{className:`su-progress-seg${n<e?` done`:n===e-1?` active`:``}`},n))})}function T({onNext:e}){let[t,n]=(0,u.useState)(``),[a,o]=(0,u.useState)(``),[s,c]=(0,u.useState)(!1),[h,y]=(0,u.useState)({email:!1,password:!1}),[b,S]=(0,u.useState)(!1),[C,w]=(0,u.useState)(null),{signup:T}=i(),E=g(a),D=p.test(t.trim().toLowerCase()),O=h.email&&t.length>0&&!D?`Enter a valid email address.`:null,k=h.password&&a.length>0&&a.length<f?`Use at least ${f} characters.`:null,A=D&&a.length>=f&&!b;async function j(n){if(n.preventDefault(),y({email:!0,password:!0}),!A)return;w(null),S(!0);let r=t.trim().toLowerCase();try{e(r,await T(r,a))}catch(e){w(e&&e.message?e.message:`Could not create your account.`),S(!1)}}return(0,d.jsxs)(`form`,{noValidate:!0,onSubmit:j,className:`su-step`,children:[(0,d.jsx)(`h2`,{className:`su-step-title`,children:`Create your Vulos account`}),(0,d.jsx)(`p`,{className:`su-step-desc`,children:`Sign up with your own email address — Gmail, Outlook, your own domain, anything. That address is how you sign in to your OS, apps and cloud console.`}),C&&(0,d.jsxs)(`div`,{role:`alert`,className:`vc-auth-alert`,"aria-live":`assertive`,children:[(0,d.jsx)(`span`,{className:`vc-auth-alert-tag`,children:`Error`}),(0,d.jsx)(`span`,{className:`vc-auth-alert-msg`,children:C})]}),(0,d.jsxs)(`div`,{className:`vc-auth-field`,children:[(0,d.jsx)(`div`,{className:`vc-auth-label-row`,children:(0,d.jsx)(`label`,{htmlFor:`su-email`,className:`vc-auth-label`,children:`Email`})}),(0,d.jsx)(`input`,{id:`su-email`,name:`email`,type:`email`,autoComplete:`email`,inputMode:`email`,spellCheck:!1,autoCapitalize:`none`,value:t,onChange:e=>{n(e.target.value),C&&w(null)},onBlur:()=>y(e=>({...e,email:!0})),placeholder:`you@example.com`,required:!0,"aria-invalid":O?`true`:`false`,"aria-describedby":O?`su-email-err`:void 0,className:`vc-auth-input${O?` has-error`:``}`,autoFocus:!0}),O&&(0,d.jsx)(`p`,{id:`su-email-err`,className:`vc-auth-fielderr`,children:O})]}),(0,d.jsxs)(`div`,{className:`vc-auth-field`,children:[(0,d.jsx)(`div`,{className:`vc-auth-label-row`,children:(0,d.jsx)(`label`,{htmlFor:`su-password`,className:`vc-auth-label`,children:`Password`})}),(0,d.jsxs)(`div`,{className:`vc-auth-input-wrap`,children:[(0,d.jsx)(`input`,{id:`su-password`,name:`password`,type:s?`text`:`password`,autoComplete:`new-password`,value:a,onChange:e=>{o(e.target.value),C&&w(null)},onBlur:()=>y(e=>({...e,password:!0})),placeholder:`At least ${f} characters`,required:!0,minLength:f,"aria-invalid":k?`true`:`false`,"aria-describedby":`su-pw-meter${k?` su-pw-err`:``}`,className:`vc-auth-input has-trailing${k?` has-error`:``}`}),(0,d.jsx)(`button`,{type:`button`,onClick:()=>c(e=>!e),className:`vc-auth-eye`,"aria-label":s?`Hide password`:`Show password`,"aria-pressed":s,children:s?`Hide`:`Show`})]}),(0,d.jsxs)(`div`,{id:`su-pw-meter`,"aria-live":`polite`,children:[(0,d.jsx)(`div`,{className:`vc-auth-meter`,"aria-hidden":a?`false`:`true`,children:[1,2,3,4].map(e=>(0,d.jsx)(`span`,{className:`vc-auth-meter-seg${E>=e?` lit-`+v[E]:``}`},e))}),a?(0,d.jsx)(`p`,{className:`vc-auth-meter-label ${v[E]}`,children:_[E]||`Too short`}):(0,d.jsxs)(`p`,{className:`vc-auth-hint`,style:{marginTop:6},children:[`Choose something only you would know — at least `,f,` characters.`]})]}),k&&(0,d.jsx)(`p`,{id:`su-pw-err`,className:`vc-auth-fielderr`,children:k})]}),(0,d.jsx)(`button`,{type:`submit`,className:`vc-auth-submit`,disabled:!A,"aria-busy":b?`true`:`false`,children:b?(0,d.jsxs)(d.Fragment,{children:[(0,d.jsx)(x,{size:14}),(0,d.jsx)(`span`,{children:`Creating account…`})]}):`Create account`}),(0,d.jsxs)(`p`,{className:`vc-auth-hint`,style:{textAlign:`center`,marginTop:4},children:[`By continuing you agree to the`,` `,(0,d.jsx)(`a`,{href:`/terms`,onClick:e=>{e.preventDefault(),r(`/terms`)},className:`vc-auth-foot-link`,children:`Terms`}),` `,`and`,` `,(0,d.jsx)(`a`,{href:`/privacy`,onClick:e=>{e.preventDefault(),r(`/privacy`)},className:`vc-auth-foot-link`,children:`Privacy Policy`}),`.`]}),(0,d.jsx)(l,{returnTo:m()})]})}function E({codes:e,onNext:t}){let[n,r]=(0,u.useState)(!1);return(0,d.jsxs)(`div`,{className:`su-step`,children:[(0,d.jsx)(`h2`,{className:`su-step-title`,children:`Save your recovery codes`}),(0,d.jsx)(`p`,{className:`su-step-desc`,children:`Normally a password-reset link goes to your email inbox. These codes are your backup for if you ever lose access to that inbox — any one of them resets your password without it. Keep them somewhere safe and separate from your email.`}),(0,d.jsx)(C,{codes:e}),(0,d.jsxs)(`label`,{className:`su-check-row`,children:[(0,d.jsx)(`input`,{type:`checkbox`,checked:n,onChange:e=>r(e.target.checked)}),(0,d.jsx)(`span`,{children:`I have saved my recovery codes somewhere safe.`})]}),(0,d.jsx)(`button`,{type:`button`,className:`vc-auth-submit`,disabled:!n,onClick:t,children:`Continue`})]})}function D({user:e,onDone:t,onSkip:n}){let r=!!e?.fleet_admin,[i,a]=(0,u.useState)(`idle`),[o,s]=(0,u.useState)(null),[c,l]=(0,u.useState)(``),[f,p]=(0,u.useState)(``),m=(0,u.useCallback)(async()=>{a(`enrolling`),p(``);try{let e=await fetch(`/api/auth/totp/enroll`,{method:`POST`,credentials:`include`,headers:{"Content-Type":`application/json`,Accept:`application/json`}}),t=await e.json().catch(()=>null);if(!e.ok)throw Error(t?.error||t?.message||`Error ${e.status}`);s(t),a(`enrolled`)}catch(e){p(e.message||`Failed to start 2FA setup. Try again.`),a(`error`)}},[]),h=(0,u.useCallback)(async()=>{let e=c.replace(/\s/g,``);if(!(e.length<6)){a(`confirming`),p(``);try{let t=await fetch(`/api/auth/totp/confirm`,{method:`POST`,credentials:`include`,headers:{"Content-Type":`application/json`,Accept:`application/json`},body:JSON.stringify({code:e})}),n=await t.json().catch(()=>null);if(!t.ok)throw Error(n?.error||n?.message||`Error ${t.status}`);a(`confirmed`)}catch(e){p(e.message||`Invalid code — check your authenticator app.`),a(`enrolled`)}}},[c]);return(0,d.jsxs)(`div`,{className:`su-step`,children:[(0,d.jsx)(`h2`,{className:`su-step-title`,children:`Secure your account`}),(0,d.jsx)(`p`,{className:`su-step-desc`,children:`Two-factor authentication adds a one-time code from your authenticator app. Takes about 60 seconds.`}),i===`idle`&&(0,d.jsxs)(`div`,{style:{display:`flex`,flexDirection:`column`,gap:12},children:[(0,d.jsx)(`div`,{className:`su-notice su-notice--good`,children:`2FA is strongly recommended.`}),(0,d.jsxs)(`div`,{className:`su-btn-row`,children:[(0,d.jsx)(`button`,{type:`button`,className:`vc-auth-submit`,onClick:m,children:`Set up 2FA`}),!r&&(0,d.jsx)(`button`,{type:`button`,className:`su-btn su-btn--ghost`,onClick:n,children:`Skip for now`})]}),r&&(0,d.jsx)(`p`,{className:`su-help su-help--warn`,children:`Fleet administrators must enable 2FA.`})]}),i===`enrolling`&&(0,d.jsxs)(`p`,{className:`su-body su-muted`,children:[(0,d.jsx)(x,{size:13}),` Preparing your authenticator secret…`]}),i===`error`&&(0,d.jsxs)(`div`,{style:{display:`flex`,flexDirection:`column`,gap:12},children:[(0,d.jsx)(`div`,{className:`su-notice su-notice--warn`,role:`alert`,children:f}),(0,d.jsx)(`button`,{type:`button`,className:`su-btn su-btn--ghost`,onClick:()=>{a(`idle`),p(``)},children:`Try again`})]}),(i===`enrolled`||i===`confirming`)&&o&&(0,d.jsxs)(`div`,{style:{display:`flex`,flexDirection:`column`,gap:16},children:[(0,d.jsxs)(`div`,{children:[(0,d.jsx)(`label`,{className:`su-label`,children:`Authenticator URI`}),(0,d.jsx)(`p`,{className:`su-help`,style:{marginBottom:8},children:`Open your authenticator app → "Add account" → "Enter setup key manually" and paste the URI.`}),(0,d.jsx)(`div`,{className:`su-uri-box`,children:(0,d.jsx)(`span`,{className:`su-uri-text`,children:o.provisioning_uri})}),(0,d.jsxs)(`div`,{style:{display:`flex`,gap:8,marginTop:8,flexWrap:`wrap`},children:[(0,d.jsx)(S,{text:o.provisioning_uri,label:`Copy URI`}),o.secret&&(0,d.jsx)(S,{text:o.secret,label:`Copy secret`})]})]}),o.recovery_codes?.length>0&&(0,d.jsx)(C,{codes:o.recovery_codes}),(0,d.jsxs)(`div`,{children:[(0,d.jsx)(`label`,{htmlFor:`su-totp-code`,className:`su-label`,children:`Enter the 6-digit code from your authenticator to activate 2FA`}),(0,d.jsxs)(`div`,{style:{display:`flex`,gap:8,flexWrap:`wrap`},children:[(0,d.jsx)(`input`,{id:`su-totp-code`,type:`text`,inputMode:`numeric`,autoComplete:`one-time-code`,placeholder:`000 000`,value:c,onChange:e=>l(e.target.value.replace(/[^0-9 ]/g,``).slice(0,7)),disabled:i===`confirming`,className:`su-totp-input`}),(0,d.jsx)(`button`,{type:`button`,className:`su-btn su-btn--primary`,onClick:h,disabled:i===`confirming`||c.replace(/\s/g,``).length<6,children:i===`confirming`?(0,d.jsxs)(d.Fragment,{children:[(0,d.jsx)(x,{size:13}),` Verifying…`]}):`Confirm & activate`})]}),f&&i!==`confirming`&&(0,d.jsx)(`div`,{className:`su-notice su-notice--warn`,role:`alert`,style:{marginTop:10},children:f})]}),!r&&(0,d.jsx)(`button`,{type:`button`,className:`su-btn su-btn--ghost su-btn--sm`,onClick:n,children:`Skip 2FA for now`})]}),i===`confirmed`&&(0,d.jsxs)(`div`,{style:{display:`flex`,flexDirection:`column`,gap:12},children:[(0,d.jsx)(`div`,{className:`su-notice su-notice--good`,role:`status`,children:`2FA enabled. Your account is protected with TOTP.`}),(0,d.jsx)(`button`,{type:`button`,className:`vc-auth-submit`,onClick:t,children:`Continue`})]})]})}function O({email:e,onNext:t}){return(0,d.jsxs)(`div`,{className:`su-step su-step--center`,children:[(0,d.jsx)(`div`,{className:`su-welcome-ring`,"aria-hidden":`true`,children:(0,d.jsx)(o,{size:56,tone:`on-dark`})}),(0,d.jsx)(`h2`,{className:`su-step-title`,children:`Your account is ready`}),(0,d.jsx)(`p`,{className:`su-step-desc`,children:`You're all set. You sign in with:`}),(0,d.jsx)(`div`,{className:`su-identity-pill`,children:(0,d.jsx)(`span`,{className:`su-identity-handle`,children:e})}),(0,d.jsx)(`p`,{className:`su-body su-muted`,style:{textAlign:`center`,maxWidth:320,margin:`0 auto`},children:`This email signs you in across every Vulos service — your OS, your apps, and your cloud console.`}),(0,d.jsx)(`button`,{type:`button`,className:`vc-auth-submit`,onClick:t,children:`Continue`})]})}var k=4;function A(){let{user:e,loading:t}=i(),[n,c]=(0,u.useState)(1),[l,f]=(0,u.useState)(``),[p,g]=(0,u.useState)([]),_=(0,u.useMemo)(()=>m(),[]);(0,u.useEffect)(()=>{!t&&e&&n===1&&h(_)},[t,e,_,n]);let v=(0,u.useCallback)((e,t)=>{f(e);let n=Array.isArray(t?.recovery_codes)?t.recovery_codes:[];g(n),c(n.length>0?2:3)},[]),y=(0,u.useCallback)(()=>c(3),[]),b=(0,u.useCallback)(()=>c(4),[]),x=(0,u.useCallback)(()=>{try{sessionStorage.removeItem(`vc:post-signup-return`)}catch{}h(_)},[_]),S=_?`/login?return=${encodeURIComponent(_)}`:`/login`;return(0,d.jsxs)(a,{slim:!0,children:[(0,d.jsx)(s,{}),(0,d.jsx)(j,{}),(0,d.jsx)(`div`,{className:`vc-auth-wrap`,children:(0,d.jsxs)(`div`,{className:`vc-auth-card vc-auth-reveal`,children:[(0,d.jsx)(`div`,{className:`vc-auth-head`,style:{marginBottom:0},children:(0,d.jsxs)(`a`,{href:`/`,onClick:e=>{e.preventDefault(),r(`/`)},className:`vc-auth-brand`,"aria-label":`Vulos — home`,children:[(0,d.jsx)(o,{size:36,tone:`on-dark`}),(0,d.jsxs)(`span`,{className:`vc-auth-wordmark`,children:[`vulos`,(0,d.jsx)(`span`,{className:`vc-auth-wordmark-suffix`,children:`.org`})]})]})}),(0,d.jsx)(w,{step:n,total:k}),n===1&&(0,d.jsx)(T,{onNext:v}),n===2&&(0,d.jsx)(E,{codes:p,onNext:y}),n===3&&(0,d.jsx)(D,{user:e,onDone:b,onSkip:b}),n===4&&(0,d.jsx)(O,{email:l,onNext:x}),n===1&&(0,d.jsx)(`div`,{className:`vc-auth-foot`,children:(0,d.jsxs)(`span`,{className:`vc-auth-foot-text`,children:[`Already have an account?`,` `,(0,d.jsx)(`a`,{href:S,onClick:e=>{e.preventDefault(),r(S)},className:`vc-auth-foot-link`,children:`Sign in`})]})})]})})]})}function j(){return(0,d.jsx)(`style`,{children:`
      /* ── Progress bar ─────────────────────────────────── */
      .su-progress-wrap {
        display: flex;
        gap: 4px;
        margin: 16px 0 24px;
      }
      .su-progress-seg {
        flex: 1;
        height: 3px;
        border-radius: 99px;
        background: var(--border-strong);
        transition: background 200ms var(--ease, ease);
      }
      .su-progress-seg.active { background: var(--accent); }
      .su-progress-seg.done   { background: var(--good, #2dd4bf); opacity: 0.7; }

      /* ── Step container ──────────────────────────────── */
      .su-step {
        display: flex;
        flex-direction: column;
        gap: 18px;
      }
      .su-step--center { align-items: center; text-align: center; }
      .su-step-title {
        font-family: var(--font-mono);
        font-size: 1.375rem;
        font-weight: 700;
        letter-spacing: -0.025em;
        color: var(--text-primary);
        margin: 0;
        line-height: 1.2;
      }
      .su-step-desc {
        font-family: var(--font-sans);
        font-size: 0.9375rem;
        color: var(--text-tertiary);
        margin: 0;
        line-height: 1.55;
      }

      /* ── Buttons ─────────────────────────────────────── */
      .su-btn {
        display: inline-flex; align-items: center; justify-content: center;
        gap: 6px; font-family: var(--font-mono); font-weight: 500;
        letter-spacing: 0.01em; line-height: 1; cursor: pointer;
        border-radius: var(--radius, 10px); border: 1px solid transparent;
        white-space: nowrap;
        transition: background 120ms ease, border-color 120ms ease, color 120ms ease, transform 120ms ease;
      }
      .su-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
      .su-btn:disabled { opacity: 0.45; cursor: not-allowed; }

      .su-btn--sm  { padding: 7px 14px; font-size: 0.8125rem; min-height: 34px; }
      .su-btn--primary {
        background: var(--accent); border-color: var(--accent); color: #fff;
        padding: 11px 22px; font-size: 0.875rem; min-height: 44px;
      }
      .su-btn--primary:hover:not(:disabled) {
        background: var(--accent-hover); border-color: var(--accent-hover);
        transform: translateY(-1px);
      }
      .su-btn--ghost {
        background: transparent; border-color: var(--border-strong); color: var(--text-tertiary);
        padding: 11px 22px; font-size: 0.875rem; min-height: 44px;
      }
      .su-btn--ghost:hover:not(:disabled) {
        color: var(--text-primary); border-color: var(--border-emphasis);
        background: rgba(255,255,255,.04);
      }

      /* ── Button rows ─────────────────────────────────── */
      .su-btn-row {
        display: flex;
        gap: 10px;
        flex-wrap: wrap;
      }
      .su-flex-1 { flex: 1; }

      /* ── Notices ─────────────────────────────────────── */
      .su-notice {
        padding: 12px 14px; border-radius: var(--radius, 10px);
        font-family: var(--font-mono); font-size: 0.8125rem; line-height: 1.55;
        border: 1px solid transparent;
      }
      .su-notice--good {
        background: rgba(45,212,191,.07); color: var(--good);
        border-color: rgba(45,212,191,.18);
      }
      .su-notice--warn {
        background: rgba(245,158,11,.07); color: var(--warn);
        border-color: rgba(245,158,11,.18);
      }

      /* ── Labels / help ───────────────────────────────── */
      .su-label {
        font-family: var(--font-mono); font-size: 0.75rem;
        color: var(--text-tertiary); display: block; margin-bottom: 6px;
      }
      .su-help {
        font-family: var(--font-mono); font-size: 0.75rem;
        color: var(--text-tertiary); line-height: 1.55; margin: 0;
      }
      .su-help--warn { color: var(--warn); }
      .su-body {
        font-family: var(--font-mono); font-size: 0.8125rem;
        color: var(--text-secondary); line-height: 1.65; margin: 0;
      }
      .su-muted { color: var(--text-tertiary); }

      /* ── URI box ─────────────────────────────────────── */
      .su-uri-box {
        background: var(--bg-elevated); border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px); padding: 12px 14px;
        overflow-x: auto; word-break: break-all;
      }
      .su-uri-text {
        font-family: var(--font-mono); font-size: 0.75rem;
        color: var(--text-secondary); line-height: 1.55; display: block;
      }

      /* ── Recovery codes ──────────────────────────────── */
      .su-code-box {
        background: var(--bg-elevated); border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px); padding: 14px;
      }
      .su-codes-grid {
        display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px 16px;
      }
      .su-code {
        font-family: var(--font-mono); font-size: 0.875rem; letter-spacing: 0.12em;
        color: var(--text-primary); padding: 4px 0;
        border-bottom: 1px solid var(--border-subtle);
      }
      .su-check-row {
        display: flex; align-items: center; gap: 10px;
        font-size: 0.875rem; color: var(--text-secondary); cursor: pointer;
      }
      .su-check-row input { accent-color: var(--accent); }

      /* ── TOTP input ──────────────────────────────────── */
      .su-totp-input {
        font-family: var(--font-mono); font-size: 1rem; letter-spacing: 0.2em;
        color: var(--text-primary); background: var(--bg-elevated);
        border: 1px solid var(--border-strong); border-radius: var(--radius, 10px);
        padding: 10px 14px; width: 140px; outline: none;
        transition: border-color 120ms ease, box-shadow 120ms ease;
      }
      .su-totp-input:focus-visible { border-color: var(--accent); box-shadow: var(--focus-ring); }
      .su-totp-input::placeholder { color: var(--text-ghost); letter-spacing: normal; }

      /* ── Welcome ring ────────────────────────────────── */
      .su-welcome-ring {
        width: 96px; height: 96px; border-radius: 50%;
        background: rgba(255,255,255,.04);
        border: 1px solid var(--border-strong);
        display: flex; align-items: center; justify-content: center;
        margin-bottom: 4px;
      }

      /* ── Identity pill (shows your own email) ────────── */
      .su-identity-pill {
        display: inline-flex; align-items: baseline; gap: 1px;
        background: var(--bg-elevated);
        border: 1px solid var(--border-strong);
        border-radius: 24px;
        padding: 8px 18px;
        font-family: var(--font-mono);
        font-size: 1rem;
        max-width: 100%;
      }
      .su-identity-handle {
        color: var(--text-primary); font-weight: 700; letter-spacing: -0.02em;
        overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
      }

      /* ── Responsive ──────────────────────────────────── */
      @media (max-width: 360px) {
        .su-btn-row { flex-direction: column; }
        .su-btn-row .su-flex-1 { width: 100%; }
      }
      @media (prefers-reduced-motion: reduce) {
        .su-btn--primary, .su-btn--ghost { transform: none !important; }
      }
    `})}export{A as default};