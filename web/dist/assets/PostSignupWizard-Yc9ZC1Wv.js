import{n as e,t}from"./jsx-runtime-Qy3n81sD.js";import{c as n,l as r,u as i}from"./index-CKoCcdrv.js";var a=e(),o=t();async function s(e){try{return await navigator.clipboard.writeText(e),!0}catch{return!1}}function c(e,t){let n=new Blob([t],{type:`text/plain`}),r=URL.createObjectURL(n),i=Object.assign(document.createElement(`a`),{href:r,download:e});document.body.appendChild(i),i.click(),document.body.removeChild(i),URL.revokeObjectURL(r)}function l({size:e=14}){return(0,o.jsx)(`span`,{"aria-hidden":`true`,style:{display:`inline-block`,width:e,height:e,borderRadius:`50%`,border:`2px solid color-mix(in srgb, currentColor 25%, transparent)`,borderTopColor:`currentColor`,animation:`pswSpin 0.7s linear infinite`}})}function u({text:e,label:t=`Copy`}){let[n,r]=(0,a.useState)(!1);return(0,o.jsx)(`button`,{type:`button`,className:`psw-btn psw-btn--ghost psw-btn--sm`,onClick:(0,a.useCallback)(async()=>{await s(e)&&(r(!0),setTimeout(()=>r(!1),2e3))},[e]),children:n?`Copied!`:t})}function d({codes:e}){let t=e.join(`
`);return(0,o.jsxs)(`div`,{className:`psw-codes-wrap`,children:[(0,o.jsxs)(`p`,{className:`psw-body`,style:{marginBottom:`10px`},children:[(0,o.jsx)(`strong`,{style:{color:`var(--warn)`},children:`Save these recovery codes now.`}),` `,`They will not be shown again. Each code can be used once if you lose your authenticator.`]}),(0,o.jsx)(`div`,{className:`psw-codes-box`,children:(0,o.jsx)(`div`,{className:`psw-codes-grid`,children:e.map((e,t)=>(0,o.jsx)(`span`,{className:`psw-code`,children:e},t))})}),(0,o.jsxs)(`div`,{className:`psw-row-gap`,children:[(0,o.jsx)(u,{text:t,label:`Copy all codes`}),(0,o.jsx)(`button`,{type:`button`,className:`psw-btn psw-btn--ghost psw-btn--sm`,onClick:()=>c(`vulos-recovery-codes.txt`,t),children:`Download as .txt`})]})]})}function f({user:e,onDone:t,onSkip:n}){let r=!!e?.fleet_admin,[i,s]=(0,a.useState)(`idle`),[c,f]=(0,a.useState)(null),[p,m]=(0,a.useState)(``),[h,g]=(0,a.useState)(``),_=(0,a.useCallback)(async()=>{s(`enrolling`),g(``);try{let e=await fetch(`/api/auth/totp/enroll`,{method:`POST`,credentials:`include`,headers:{"Content-Type":`application/json`,Accept:`application/json`}}),t=await e.json().catch(()=>null);if(!e.ok)throw Error(t?.error||t?.message||`Error ${e.status}`);f(t),s(`enrolled`)}catch(e){g(e.message||`Failed to start 2FA setup. Please try again.`),s(`error`)}},[]),v=(0,a.useCallback)(async()=>{let e=p.replace(/\s/g,``);if(!(e.length<6)){s(`confirming`),g(``);try{let t=await fetch(`/api/auth/totp/confirm`,{method:`POST`,credentials:`include`,headers:{"Content-Type":`application/json`,Accept:`application/json`},body:JSON.stringify({code:e})}),n=await t.json().catch(()=>null);if(!t.ok)throw Error(n?.error||n?.message||`Error ${t.status}`);s(`confirmed`)}catch(e){g(e.message||`Invalid code — check your authenticator app and try again.`),s(`enrolled`)}}},[p]);return(0,o.jsxs)(`div`,{className:`psw-step`,children:[(0,o.jsxs)(`div`,{className:`psw-step-header`,children:[(0,o.jsx)(`div`,{className:`psw-step-badge`,children:`Step 1 of 2`}),(0,o.jsx)(`h2`,{className:`psw-step-title`,children:`Set up two-factor authentication`}),(0,o.jsx)(`p`,{className:`psw-step-desc`,children:`2FA protects your account with a one-time code from your authenticator app (Google Authenticator, Authy, 1Password, etc.).`})]}),i===`idle`&&(0,o.jsxs)(`div`,{className:`psw-body-area`,children:[(0,o.jsxs)(`div`,{className:`psw-notice psw-notice--good`,role:`status`,children:[(0,o.jsx)(`span`,{className:`psw-notice-dot`,"aria-hidden":`true`}),`Recommended — takes about 60 seconds.`]}),(0,o.jsxs)(`div`,{className:`psw-actions`,children:[(0,o.jsx)(`button`,{type:`button`,className:`psw-btn psw-btn--primary`,onClick:_,children:`Set up 2FA now`}),!r&&(0,o.jsx)(`button`,{type:`button`,className:`psw-btn psw-btn--ghost`,onClick:n,children:`Skip for now`})]}),r&&(0,o.jsx)(`p`,{className:`psw-help psw-help--warn`,children:`Fleet administrators must enable 2FA. The skip option is not available for your account type.`})]}),i===`enrolling`&&(0,o.jsx)(`div`,{className:`psw-body-area`,children:(0,o.jsxs)(`p`,{className:`psw-body psw-muted`,children:[(0,o.jsx)(l,{size:13}),` Preparing your authenticator secret…`]})}),i===`error`&&(0,o.jsxs)(`div`,{className:`psw-body-area`,children:[(0,o.jsxs)(`div`,{className:`psw-notice psw-notice--warn`,role:`alert`,children:[(0,o.jsx)(`span`,{className:`psw-notice-dot`,"aria-hidden":`true`}),h]}),(0,o.jsx)(`div`,{className:`psw-actions`,children:(0,o.jsx)(`button`,{type:`button`,className:`psw-btn psw-btn--ghost`,onClick:()=>{s(`idle`),g(``)},children:`Try again`})})]}),(i===`enrolled`||i===`confirming`)&&c&&(0,o.jsxs)(`div`,{className:`psw-body-area`,children:[(0,o.jsxs)(`div`,{className:`psw-uri-section`,children:[(0,o.jsx)(`label`,{className:`psw-label`,children:`Authenticator URI`}),(0,o.jsx)(`p`,{className:`psw-help`,style:{marginBottom:`8px`},children:`Open your authenticator app, tap "Add account" → "Enter setup key manually" (or scan a QR), then paste the URI below.`}),(0,o.jsx)(`div`,{className:`psw-uri-box`,children:(0,o.jsx)(`span`,{className:`psw-uri-text`,children:c.provisioning_uri})}),(0,o.jsxs)(`div`,{className:`psw-row-gap`,style:{marginTop:`8px`},children:[(0,o.jsx)(u,{text:c.provisioning_uri,label:`Copy URI`}),c.secret&&(0,o.jsx)(u,{text:c.secret,label:`Copy secret`})]})]}),c.recovery_codes&&c.recovery_codes.length>0&&(0,o.jsx)(d,{codes:c.recovery_codes}),(0,o.jsxs)(`div`,{className:`psw-confirm-section`,children:[(0,o.jsx)(`label`,{htmlFor:`psw-totp-code`,className:`psw-label`,children:`Enter the 6-digit code from your authenticator to activate 2FA`}),(0,o.jsxs)(`div`,{className:`psw-confirm-row`,children:[(0,o.jsx)(`input`,{id:`psw-totp-code`,type:`text`,inputMode:`numeric`,autoComplete:`one-time-code`,placeholder:`000 000`,value:p,onChange:e=>m(e.target.value.replace(/[^0-9 ]/g,``).slice(0,7)),disabled:i===`confirming`,className:`psw-input`,style:{maxWidth:`160px`}}),(0,o.jsx)(`button`,{type:`button`,className:`psw-btn psw-btn--primary`,onClick:v,disabled:i===`confirming`||p.replace(/\s/g,``).length<6,children:i===`confirming`?(0,o.jsxs)(o.Fragment,{children:[(0,o.jsx)(l,{size:13}),` Verifying…`]}):`Confirm & activate`})]}),h&&i!==`confirming`&&(0,o.jsxs)(`div`,{className:`psw-notice psw-notice--warn`,role:`alert`,style:{marginTop:`10px`},children:[(0,o.jsx)(`span`,{className:`psw-notice-dot`,"aria-hidden":`true`}),h]})]}),!r&&(0,o.jsx)(`div`,{style:{paddingTop:`4px`},children:(0,o.jsx)(`button`,{type:`button`,className:`psw-btn psw-btn--ghost psw-btn--sm`,onClick:n,children:`Skip 2FA for now`})})]}),i===`confirmed`&&(0,o.jsxs)(`div`,{className:`psw-body-area`,children:[(0,o.jsxs)(`div`,{className:`psw-notice psw-notice--good`,role:`status`,children:[(0,o.jsx)(`span`,{className:`psw-notice-dot`,"aria-hidden":`true`}),`2FA enabled. Your account is now protected with TOTP.`]}),(0,o.jsx)(`div`,{className:`psw-actions`,style:{paddingTop:`8px`},children:(0,o.jsx)(`button`,{type:`button`,className:`psw-btn psw-btn--primary`,onClick:t,children:`Continue`})})]})]})}var p=30;function m({user:e,onDone:t}){let n=e?.email??``,r=!!e?.email_verified,[i,s]=(0,a.useState)(`idle`),[c,u]=(0,a.useState)(``),[d,f]=(0,a.useState)(0),m=(0,a.useRef)(null);(0,a.useEffect)(()=>()=>{m.current&&clearInterval(m.current)},[]);let h=(0,a.useCallback)(async()=>{s(`sending`),u(``);try{let e=await fetch(`/api/auth/verify-email/resend`,{method:`POST`,credentials:`include`,headers:{"Content-Type":`application/json`,Accept:`application/json`}});if(!e.ok&&e.status!==204){let t=await e.json().catch(()=>null);throw Error(t?.error||t?.message||`Error ${e.status}`)}s(`sent`);let t=p;f(t),m.current=setInterval(()=>{--t,f(t),t<=0&&(clearInterval(m.current),s(`idle`),f(0))},1e3)}catch(e){u(e.message||`Could not resend. Please try again shortly.`),s(`error`)}},[]);return(0,o.jsxs)(`div`,{className:`psw-step`,children:[(0,o.jsxs)(`div`,{className:`psw-step-header`,children:[(0,o.jsx)(`div`,{className:`psw-step-badge`,children:`Step 2 of 2`}),(0,o.jsx)(`h2`,{className:`psw-step-title`,children:`Verify your email`}),(0,o.jsx)(`p`,{className:`psw-step-desc`,children:`We sent a verification link to your inbox. Verifying your email unlocks all account features.`})]}),(0,o.jsxs)(`div`,{className:`psw-body-area`,children:[r?(0,o.jsxs)(`div`,{className:`psw-notice psw-notice--good`,role:`status`,children:[(0,o.jsx)(`span`,{className:`psw-notice-dot`,"aria-hidden":`true`}),`Email verified — you're all set!`]}):(0,o.jsxs)(o.Fragment,{children:[(0,o.jsxs)(`div`,{className:`psw-notice psw-notice--faint`,role:`status`,children:[(0,o.jsx)(`span`,{className:`psw-notice-dot`,"aria-hidden":`true`}),`Check your inbox for`,` `,(0,o.jsx)(`span`,{className:`psw-mono`,children:n||`your email address`}),`.`,` `,`If you don't see it, check your spam folder.`]}),c&&(0,o.jsxs)(`div`,{className:`psw-notice psw-notice--warn`,role:`alert`,children:[(0,o.jsx)(`span`,{className:`psw-notice-dot`,"aria-hidden":`true`}),c]}),i===`sent`&&(0,o.jsxs)(`div`,{className:`psw-notice psw-notice--good`,role:`status`,children:[(0,o.jsx)(`span`,{className:`psw-notice-dot`,"aria-hidden":`true`}),`Verification email resent. Check your inbox.`]}),(0,o.jsx)(`div`,{className:`psw-actions`,children:(0,o.jsx)(`button`,{type:`button`,className:`psw-btn psw-btn--ghost psw-btn--sm`,onClick:h,disabled:i===`sending`||d>0,children:i===`sending`?(0,o.jsxs)(o.Fragment,{children:[(0,o.jsx)(l,{size:12}),` Sending…`]}):d>0?`Resend in ${d}s`:`Resend verification email`})}),(0,o.jsx)(`p`,{className:`psw-help`,style:{marginTop:`8px`},children:`You can continue to your dashboard now — you'll see a reminder until your email is verified.`})]}),(0,o.jsx)(`div`,{className:`psw-actions`,style:{paddingTop:`8px`,borderTop:`1px solid var(--border-subtle)`,marginTop:`8px`},children:(0,o.jsx)(`button`,{type:`button`,className:`psw-btn psw-btn--primary`,onClick:t,children:r?`Go to dashboard`:`Continue to dashboard`})})]})]})}function h(){let{user:e,loading:t,refresh:s}=r(),[c,u]=(0,a.useState)(`2fa`);(0,a.useEffect)(()=>{!t&&!e&&i(`/login`)},[t,e]);let d=(0,a.useCallback)(async()=>{await s(),u(`email`)},[s]),p=(0,a.useCallback)(()=>{i(`/`)},[]);return t?(0,o.jsx)(`div`,{className:`psw-page`,style:{display:`flex`,alignItems:`center`,justifyContent:`center`,minHeight:`100dvh`},children:(0,o.jsx)(l,{size:20})}):e?(0,o.jsxs)(o.Fragment,{children:[(0,o.jsx)(g,{}),(0,o.jsx)(`div`,{className:`psw-page`,children:(0,o.jsxs)(`div`,{className:`psw-card psw-reveal`,children:[(0,o.jsx)(`div`,{className:`psw-brand-row`,children:(0,o.jsxs)(`button`,{type:`button`,className:`psw-brand-link`,onClick:()=>i(`/`),"aria-label":`Vulos — home`,children:[(0,o.jsx)(n,{size:32,tone:`on-dark`}),(0,o.jsxs)(`span`,{className:`psw-wordmark`,children:[`vulos`,(0,o.jsx)(`span`,{className:`psw-wordmark-suffix`,children:`.org`})]})]})}),(0,o.jsxs)(`div`,{className:`psw-progress`,"aria-label":`Wizard progress`,children:[(0,o.jsx)(`span`,{className:`psw-dot${c===`2fa`?` psw-dot--active`:` psw-dot--done`}`,"aria-label":`Step 1: 2FA setup`}),(0,o.jsx)(`span`,{className:`psw-dot${c===`email`?` psw-dot--active`:``}`,"aria-label":`Step 2: Email verification`})]}),c===`2fa`&&(0,o.jsx)(f,{user:e,onDone:d,onSkip:d}),c===`email`&&(0,o.jsx)(m,{user:e,onDone:p})]})})]}):null}function g(){return(0,o.jsx)(`style`,{children:`
      @keyframes pswSpin {
        to { transform: rotate(360deg); }
      }
      @keyframes pswReveal {
        from { opacity: 0; transform: translateY(10px); }
        to   { opacity: 1; transform: translateY(0); }
      }

      /* ── Page ────────────────────────────────────────── */
      .psw-page {
        min-height: 100dvh;
        display: flex;
        align-items: flex-start;
        justify-content: center;
        padding: 64px 16px 80px;
        background: var(--bg-base);
      }

      /* ── Card ────────────────────────────────────────── */
      .psw-card {
        width: 100%;
        max-width: 520px;
        background: var(--bg-surface);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius-lg);
        padding: 32px;
        display: flex;
        flex-direction: column;
        gap: 24px;
      }
      .psw-reveal {
        animation: pswReveal 240ms var(--ease, ease) both;
      }

      /* ── Brand row ───────────────────────────────────── */
      .psw-brand-row {
        display: flex;
        justify-content: center;
      }
      .psw-brand-link {
        display: inline-flex;
        align-items: center;
        gap: 10px;
        text-decoration: none;
        background: none;
        border: none;
        cursor: pointer;
        padding: 0;
      }
      .psw-wordmark {
        font-family: var(--font-mono);
        font-size: 1.125rem;
        font-weight: 700;
        letter-spacing: -0.03em;
        color: var(--text-primary);
        line-height: 1;
      }
      .psw-wordmark-suffix {
        color: var(--text-faint);
        font-weight: 400;
      }

      /* ── Progress dots ───────────────────────────────── */
      .psw-progress {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
      }
      .psw-dot {
        display: inline-block;
        width: 8px;
        height: 8px;
        border-radius: 50%;
        background: var(--border-strong);
        transition: background 200ms ease, transform 200ms ease;
      }
      .psw-dot--active {
        background: var(--accent);
        transform: scale(1.25);
      }
      .psw-dot--done {
        background: var(--good, #2dd4bf);
        opacity: 0.7;
      }

      /* ── Step ────────────────────────────────────────── */
      .psw-step {
        display: flex;
        flex-direction: column;
        gap: 20px;
      }
      .psw-step-header {
        display: flex;
        flex-direction: column;
        gap: 8px;
      }
      .psw-step-badge {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 500;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--accent);
        line-height: 1;
      }
      .psw-step-title {
        font-family: var(--font-mono);
        font-size: 1.125rem;
        font-weight: 700;
        letter-spacing: -0.02em;
        color: var(--text-primary);
        margin: 0;
        line-height: 1.3;
      }
      .psw-step-desc {
        font-family: var(--font-mono);
        font-size: 0.8125rem;
        color: var(--text-tertiary);
        line-height: 1.6;
        margin: 0;
      }

      /* ── Body area ───────────────────────────────────── */
      .psw-body-area {
        display: flex;
        flex-direction: column;
        gap: 16px;
      }
      .psw-body {
        font-family: var(--font-mono);
        font-size: 0.8125rem;
        color: var(--text-secondary);
        line-height: 1.65;
        margin: 0;
      }
      .psw-muted { color: var(--text-tertiary); }
      .psw-mono  { font-family: var(--font-mono); }

      /* ── Label ───────────────────────────────────────── */
      .psw-label {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--text-tertiary);
        display: block;
        margin-bottom: 6px;
      }

      /* ── Input ───────────────────────────────────────── */
      .psw-input {
        width: 100%;
        font-family: var(--font-mono);
        font-size: 0.875rem;
        color: var(--text-primary);
        background: var(--bg-elevated);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius);
        padding: 10px 14px;
        transition:
          border-color var(--dur-fast, 120ms) var(--ease, ease),
          box-shadow   var(--dur-fast, 120ms) var(--ease, ease);
        outline: none;
        letter-spacing: 0.15em;
      }
      .psw-input:focus-visible {
        border-color: var(--accent);
        box-shadow: var(--focus-ring);
      }
      .psw-input::placeholder { color: var(--text-muted); letter-spacing: normal; }
      .psw-input:disabled { opacity: 0.5; cursor: not-allowed; }

      /* ── Buttons ─────────────────────────────────────── */
      .psw-btn {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 6px;
        font-family: var(--font-mono);
        font-weight: 500;
        letter-spacing: 0.01em;
        line-height: 1;
        cursor: pointer;
        border-radius: var(--radius);
        border: 1px solid transparent;
        white-space: nowrap;
        transition:
          background   var(--dur-fast, 120ms) var(--ease, ease),
          border-color var(--dur-fast, 120ms) var(--ease, ease),
          color        var(--dur-fast, 120ms) var(--ease, ease),
          transform    var(--dur-fast, 120ms) var(--ease, ease);
      }
      .psw-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
      .psw-btn:disabled { opacity: 0.45; cursor: not-allowed; }
      .psw-btn:disabled:hover { transform: none; }

      .psw-btn--sm { padding: 7px 14px; font-size: 0.8125rem; min-height: 34px; }
      .psw-btn--md { padding: 11px 22px; font-size: 0.875rem;  min-height: 44px; }

      .psw-btn--primary {
        background: var(--accent);
        border-color: var(--accent);
        color: #fff;
        padding: 11px 22px;
        font-size: 0.875rem;
        min-height: 44px;
      }
      .psw-btn--primary:hover:not(:disabled) {
        background: var(--accent-hover);
        border-color: var(--accent-hover);
        transform: translateY(-1px);
      }
      .psw-btn--primary:active { transform: translateY(0); }

      .psw-btn--ghost {
        background: transparent;
        border-color: var(--border-strong);
        color: var(--text-tertiary);
      }
      .psw-btn--ghost:hover:not(:disabled) {
        color: var(--text-primary);
        border-color: var(--border-emphasis);
        background: rgba(255,255,255,.04);
        transform: translateY(-1px);
      }
      .psw-btn--ghost:active { transform: translateY(0); }

      /* ── Actions row ─────────────────────────────────── */
      .psw-actions {
        display: flex;
        align-items: center;
        gap: 10px;
        flex-wrap: wrap;
      }

      .psw-row-gap {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
      }

      /* ── Notices ─────────────────────────────────────── */
      .psw-notice {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        padding: 12px 14px;
        border-radius: var(--radius);
        font-family: var(--font-mono);
        font-size: 0.8125rem;
        line-height: 1.55;
        border: 1px solid transparent;
      }
      .psw-notice--good {
        background: rgba(45,212,191,.07);
        color: var(--good);
        border-color: rgba(45,212,191,.18);
      }
      .psw-notice--warn {
        background: rgba(245,158,11,.07);
        color: var(--warn);
        border-color: rgba(245,158,11,.18);
      }
      .psw-notice--faint {
        background: rgba(255,255,255,.03);
        color: var(--text-secondary);
        border-color: var(--border-strong);
      }
      .psw-notice-dot {
        display: inline-block;
        width: 6px;
        height: 6px;
        border-radius: 50%;
        background: currentColor;
        flex-shrink: 0;
        margin-top: 5px;
        opacity: 0.75;
      }

      /* ── Help text ───────────────────────────────────── */
      .psw-help {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--text-tertiary);
        line-height: 1.55;
      }
      .psw-help--warn { color: var(--warn); }

      /* ── URI box ─────────────────────────────────────── */
      .psw-uri-section { display: flex; flex-direction: column; }
      .psw-uri-box {
        background: var(--bg-elevated);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius);
        padding: 12px 14px;
        overflow-x: auto;
        word-break: break-all;
      }
      .psw-uri-text {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--text-secondary);
        line-height: 1.55;
        display: block;
      }

      /* ── Recovery codes ──────────────────────────────── */
      .psw-codes-wrap {
        display: flex;
        flex-direction: column;
        gap: 10px;
      }
      .psw-codes-box {
        background: var(--bg-elevated);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius);
        padding: 14px;
      }
      .psw-codes-grid {
        display: grid;
        grid-template-columns: repeat(2, 1fr);
        gap: 6px 16px;
      }
      .psw-code {
        font-family: var(--font-mono);
        font-size: 0.875rem;
        letter-spacing: 0.12em;
        color: var(--text-primary);
        padding: 4px 0;
        border-bottom: 1px solid var(--border-subtle);
      }

      /* ── Confirm row ─────────────────────────────────── */
      .psw-confirm-section { display: flex; flex-direction: column; }
      .psw-confirm-row {
        display: flex;
        align-items: stretch;
        gap: 8px;
        flex-wrap: wrap;
      }

      /* ── Responsive ──────────────────────────────────── */
      @media (max-width: 560px) {
        .psw-page   { padding: 40px 12px 64px; }
        .psw-card   { padding: 24px 18px; gap: 20px; }
        .psw-codes-grid { grid-template-columns: 1fr; }
        .psw-confirm-row { flex-direction: column; }
        .psw-confirm-row .psw-input { max-width: 100% !important; }
        .psw-btn--primary { width: 100%; }
      }
      @media (max-width: 360px) {
        .psw-page   { padding: 28px 10px 56px; }
        .psw-card   { padding: 20px 14px; }
      }
      @media (prefers-reduced-motion: reduce) {
        .psw-reveal { animation: none; }
      }
    `})}export{h as default};