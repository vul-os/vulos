import{n as e,t}from"./jsx-runtime-Qy3n81sD.js";import{c as n,l as r,o as i,s as a,u as o}from"./index-XyxKVgkH.js";import{i as s}from"./ui-J8a9L6bi.js";var c=e(),l=t();function u(e){return!e||typeof e!=`string`?``:e.trim().toUpperCase()}function d(){return`/login?return=${encodeURIComponent(o())}`}var f=/^[A-Z0-9]{4}-[A-Z0-9]{4}$/;function p(e){let t=e.toUpperCase().replace(/[^A-Z0-9]/g,``);return t.length<=4?t:`${t.slice(0,4)}-${t.slice(4,8)}`}async function m(e){try{let t=await e.text();if(!t)return null;let n=JSON.parse(t);return n&&typeof n.error==`string`?n.error:n&&typeof n.message==`string`?n.message:t||null}catch{return null}}function h({size:e=14}){return(0,l.jsx)(`span`,{"aria-hidden":`true`,style:{display:`inline-block`,width:e,height:e,borderRadius:`50%`,border:`2px solid color-mix(in srgb, currentColor 25%, transparent)`,borderTopColor:`currentColor`,animation:`vcActSpin 0.7s linear infinite`,flexShrink:0}})}function g(){return(0,l.jsx)(`div`,{style:{minHeight:`60dvh`,display:`flex`,alignItems:`center`,justifyContent:`center`,padding:`var(--sp-6, 48px) var(--sp-3, 24px)`},children:(0,l.jsxs)(`span`,{style:{display:`inline-flex`,alignItems:`center`,gap:10,fontFamily:`var(--font-mono)`,fontSize:`0.8125rem`,letterSpacing:`0.02em`,color:`var(--text-faint)`},children:[(0,l.jsx)(`span`,{"aria-hidden":`true`,style:{width:6,height:6,borderRadius:`50%`,background:`var(--accent)`,animation:`vcActPulse 1.1s ease-in-out infinite`,flexShrink:0}}),`Checking session…`]})})}function _({userCode:e,onActivateAnother:t}){return(0,l.jsxs)(`div`,{className:`vca-card vca-reveal`,children:[(0,l.jsx)(y,{eyebrow:`Device Enrollment`,title:`Device approved`}),(0,l.jsxs)(`div`,{className:`vca-ok`,role:`status`,"aria-live":`polite`,children:[(0,l.jsx)(`span`,{className:`vca-ok-tag`,children:`Approved`}),(0,l.jsxs)(`span`,{className:`vca-ok-msg`,children:[`The device with code`,` `,(0,l.jsx)(`code`,{className:`vca-code-inline`,children:e}),` has been enrolled in your fleet. It will complete activation within a few seconds.`]})]}),(0,l.jsxs)(`p`,{style:{fontFamily:`var(--font-sans)`,fontSize:`0.875rem`,color:`var(--text-tertiary)`,lineHeight:1.6,margin:`var(--sp-3, 24px) 0 0`},children:[`New to bare-metal boot?`,` `,(0,l.jsx)(`a`,{href:`/first-boot`,onClick:e=>{e.preventDefault(),n(`/first-boot`)},style:{color:`var(--text-secondary)`,textDecoration:`underline`,textUnderlineOffset:`2px`},children:`See the first-boot guide`}),` `,`to complete your first Vulos install.`]}),(0,l.jsx)(`button`,{type:`button`,className:`vca-btn-ghost`,style:{marginTop:`var(--sp-3, 24px)`,width:`100%`},onClick:t,children:`Activate another device`})]})}function v({userCode:e,onActivateAnother:t}){return(0,l.jsxs)(`div`,{className:`vca-card vca-reveal`,children:[(0,l.jsx)(y,{eyebrow:`Device Enrollment`,title:`Device denied`}),(0,l.jsxs)(`div`,{className:`vca-warn`,role:`status`,"aria-live":`polite`,children:[(0,l.jsx)(`span`,{className:`vca-warn-tag`,children:`Denied`}),(0,l.jsxs)(`span`,{className:`vca-warn-msg`,children:[`The request from`,` `,(0,l.jsx)(`code`,{className:`vca-code-inline`,children:e}),` was denied. The device will not be added to your fleet.`]})]}),(0,l.jsx)(`button`,{type:`button`,className:`vca-btn-ghost`,style:{marginTop:`var(--sp-3, 24px)`,width:`100%`},onClick:t,children:`Go back`})]})}function y({eyebrow:e,title:t,subtitle:r}){return(0,l.jsxs)(`div`,{className:`vca-head`,children:[(0,l.jsxs)(`a`,{href:`/`,onClick:e=>{e.preventDefault(),n(`/`)},className:`vca-brand`,"aria-label":`Vulos — home`,children:[(0,l.jsx)(i,{size:36,tone:`on-dark`}),(0,l.jsxs)(`span`,{className:`vca-wordmark`,children:[`vulos`,(0,l.jsx)(`span`,{className:`vca-wordmark-suffix`,children:`.org`})]})]}),e&&(0,l.jsx)(`p`,{className:`vca-eyebrow`,children:e}),t&&(0,l.jsx)(`h1`,{className:`vca-title`,children:t}),r&&(0,l.jsx)(`p`,{className:`vca-subtitle`,children:r})]})}function b({initialCode:e,onApproved:t,onDenied:n}){let[r,i]=(0,c.useState)(u(e)),[a,o]=(0,c.useState)(!1),[s,d]=(0,c.useState)(!1),[g,_]=(0,c.useState)(null),v=(0,c.useRef)(null);(0,c.useEffect)(()=>{!e&&v.current&&v.current.focus()},[e]);let b=a&&!f.test(r)?`Enter a valid device code in XXXX-XXXX format.`:null,x=f.test(r)&&!s;function S(e){let t=p(e.target.value);i(t),g&&_(null)}async function C(e){if(o(!0),f.test(r)){_(null),d(!0);try{let i=await fetch(e,{method:`POST`,credentials:`include`,headers:{"Content-Type":`application/json`,Accept:`application/json`},body:JSON.stringify({user_code:r})});if(i.ok||i.status===204){e.endsWith(`approve`)?t(r):n(r);return}let a=await m(i);a?a.includes(`expired_token`)||a.includes(`expired`)?a=`This device code has expired. Ask the device to restart enrollment and scan a new QR code.`:a.includes(`access_denied`)||a.includes(`denied`)?a=`Access denied. The device request has already been denied.`:(a.includes(`unknown_user_code`)||a.includes(`unknown user_code`))&&(a=`Device code not found. Check the code and try again.`):a=i.status===410||a&&a.includes(`expired`)?`This device code has expired. Ask the device to restart enrollment and scan a new QR code.`:i.status===403||a&&a.includes(`denied`)?`Access denied. The device request has already been denied.`:i.status===404||a&&a.includes(`unknown`)?`Device code not found. Check the code and try again.`:i.status===409?`This enrollment has already been processed.`:i.status===429?`Too many attempts — please wait a moment and try again.`:i.status>=500?`Something went wrong on our end. Please try again.`:`Request failed. Please try again.`,_(a)}catch(e){_(e&&e.message?e.message:`Network error. Please check your connection.`)}finally{d(!1)}}}return(0,l.jsxs)(`div`,{className:`vca-card vca-reveal`,children:[(0,l.jsx)(y,{eyebrow:`Device Enrollment`,title:`Approve device`,subtitle:`Enter the code shown on your device display to add it to your fleet.`}),(0,l.jsxs)(`form`,{noValidate:!0,onSubmit:e=>{e.preventDefault(),C(`/api/enroll/approve`)},className:`vca-form`,children:[g&&(0,l.jsxs)(`div`,{role:`alert`,"aria-live":`assertive`,className:`vca-alert`,children:[(0,l.jsx)(`span`,{className:`vca-alert-tag`,children:`Error`}),(0,l.jsx)(`span`,{className:`vca-alert-msg`,children:g})]}),(0,l.jsxs)(`div`,{className:`vca-field`,children:[(0,l.jsx)(`div`,{className:`vca-label-row`,children:(0,l.jsx)(`label`,{htmlFor:`activate-code`,className:`vca-label`,children:`Device code`})}),(0,l.jsx)(`input`,{ref:v,id:`activate-code`,name:`user_code`,type:`text`,autoComplete:`off`,spellCheck:!1,autoCapitalize:`characters`,value:r,onChange:S,onBlur:()=>o(!0),placeholder:`WXYZ-1234`,maxLength:9,required:!0,"aria-invalid":b?`true`:`false`,"aria-describedby":b?`activate-code-err`:`activate-code-hint`,className:`vca-input vca-input-code${b?` has-error`:``}`,autoFocus:!!e,disabled:s}),b?(0,l.jsx)(`p`,{id:`activate-code-err`,className:`vca-fielderr`,children:b}):(0,l.jsxs)(`p`,{id:`activate-code-hint`,className:`vca-hint`,children:[`Format: 4 characters, dash, 4 characters — e.g.`,` `,(0,l.jsx)(`code`,{className:`vca-code-inline`,children:`WXYZ-1234`})]})]}),(0,l.jsxs)(`div`,{className:`vca-summary`,"aria-label":`Approval summary`,children:[(0,l.jsxs)(`div`,{className:`vca-summary-row`,children:[(0,l.jsx)(`span`,{className:`vca-summary-key`,children:`Action`}),(0,l.jsx)(`span`,{className:`vca-summary-val`,children:`Add device to fleet`})]}),(0,l.jsxs)(`div`,{className:`vca-summary-row`,children:[(0,l.jsx)(`span`,{className:`vca-summary-key`,children:`Code`}),(0,l.jsx)(`span`,{className:`vca-summary-val vca-summary-code`,children:f.test(r)?r:(0,l.jsx)(`span`,{style:{color:`var(--text-ghost)`},children:`—`})})]}),(0,l.jsxs)(`div`,{className:`vca-summary-row`,children:[(0,l.jsx)(`span`,{className:`vca-summary-key`,children:`Time`}),(0,l.jsx)(`span`,{className:`vca-summary-val`,children:new Date().toLocaleTimeString()})]})]}),(0,l.jsx)(`button`,{type:`submit`,className:`vca-btn-approve`,disabled:!x,"aria-busy":s?`true`:`false`,children:s?(0,l.jsxs)(l.Fragment,{children:[(0,l.jsx)(h,{size:14}),(0,l.jsx)(`span`,{children:`Processing…`})]}):`Approve device`}),(0,l.jsx)(`button`,{type:`button`,className:`vca-btn-deny`,disabled:s||!f.test(r),onClick:e=>{e.preventDefault(),C(`/api/enroll/deny`)},"aria-busy":s?`true`:`false`,children:`Deny`})]})]})}function x(){return(0,l.jsx)(`style`,{children:`
      @keyframes vcActSpin  { to { transform: rotate(360deg); } }
      @keyframes vcActPulse {
        0%, 100% { opacity: 1;    transform: scale(1); }
        50%      { opacity: 0.35; transform: scale(0.65); }
      }
      @keyframes vcActRise {
        from { opacity: 0; transform: translateY(8px); }
        to   { opacity: 1; transform: translateY(0); }
      }

      /* ── Layout ─────────────────────────────────────────── */
      .vca-wrap {
        display: flex;
        align-items: flex-start;
        justify-content: center;
        width: 100%;
        padding-top: var(--sp-3, 24px);
      }
      .vca-card {
        width: 100%;
        max-width: 440px;
        background: var(--bg-surface);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius-lg, 16px);
        padding: var(--sp-6, 48px) var(--sp-5, 40px);
        box-shadow: var(--shadow);
      }
      .vca-reveal {
        animation: vcActRise 280ms var(--ease, cubic-bezier(.22,1,.36,1)) both;
      }
      @media (prefers-reduced-motion: reduce) {
        .vca-reveal { animation: none; }
      }

      /* ── Head ───────────────────────────────────────────── */
      .vca-head {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 6px;
        margin-bottom: var(--sp-4, 32px);
      }
      .vca-brand {
        display: inline-flex;
        align-items: center;
        gap: 12px;
        text-decoration: none;
        margin-bottom: var(--sp-2-5, 20px);
        padding: 2px;
        margin-left: -2px;
        border-radius: var(--radius-sm, 6px);
        transition: opacity var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vca-brand:hover { opacity: 0.86; }
      .vca-brand:focus-visible {
        outline: none;
        box-shadow: var(--focus-ring);
      }
      .vca-wordmark {
        font-family: var(--font-mono);
        font-weight: 600;
        font-size: 0.9375rem;
        letter-spacing: -0.01em;
        color: var(--text-primary);
        line-height: 1;
      }
      .vca-wordmark-suffix {
        color: var(--text-faint);
        font-weight: 400;
      }
      .vca-eyebrow {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 500;
        letter-spacing: 0.16em;
        text-transform: uppercase;
        color: var(--text-faint);
        margin: 0;
      }
      .vca-title {
        font-family: var(--font-mono);
        font-size: 1.625rem;
        font-weight: 700;
        letter-spacing: -0.025em;
        color: var(--text-primary);
        line-height: 1.15;
        margin: 4px 0 0;
      }
      .vca-subtitle {
        font-family: var(--font-sans);
        font-size: 0.9375rem;
        color: var(--text-tertiary);
        line-height: 1.55;
        margin: 8px 0 0;
      }

      /* ── Form ───────────────────────────────────────────── */
      .vca-form {
        display: flex;
        flex-direction: column;
        gap: 16px;
      }

      /* ── Alert ──────────────────────────────────────────── */
      .vca-alert {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        padding: 12px 14px;
        border-radius: var(--radius, 10px);
        background: color-mix(in srgb, var(--warn) 10%, var(--bg-elevated));
        border: 1px solid color-mix(in srgb, var(--warn) 35%, var(--border-emphasis));
      }
      .vca-alert-tag {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 600;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--warn);
        padding-top: 1px;
        flex-shrink: 0;
      }
      .vca-alert-msg {
        font-family: var(--font-sans);
        font-size: 0.875rem;
        color: var(--text-secondary);
        line-height: 1.5;
      }

      /* ── Ok (success) banner ────────────────────────────── */
      .vca-ok {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        padding: 14px 16px;
        border-radius: var(--radius, 10px);
        background: color-mix(in srgb, var(--good) 8%, var(--bg-elevated));
        border: 1px solid color-mix(in srgb, var(--good) 30%, var(--border-emphasis));
      }
      .vca-ok-tag {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 600;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--good);
        padding-top: 1px;
        flex-shrink: 0;
      }
      .vca-ok-msg {
        font-family: var(--font-sans);
        font-size: 0.875rem;
        color: var(--text-secondary);
        line-height: 1.55;
      }

      /* ── Warn (denied) banner ───────────────────────────── */
      .vca-warn {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        padding: 14px 16px;
        border-radius: var(--radius, 10px);
        background: color-mix(in srgb, var(--warn) 8%, var(--bg-elevated));
        border: 1px solid color-mix(in srgb, var(--warn) 28%, var(--border-emphasis));
      }
      .vca-warn-tag {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 600;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--warn);
        padding-top: 1px;
        flex-shrink: 0;
      }
      .vca-warn-msg {
        font-family: var(--font-sans);
        font-size: 0.875rem;
        color: var(--text-secondary);
        line-height: 1.55;
      }

      /* ── Field ──────────────────────────────────────────── */
      .vca-field {
        display: flex;
        flex-direction: column;
        gap: 6px;
      }
      .vca-label-row {
        display: flex;
        align-items: baseline;
        justify-content: space-between;
        gap: 12px;
      }
      .vca-label {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        font-weight: 500;
        letter-spacing: 0.06em;
        text-transform: uppercase;
        color: var(--text-tertiary);
        line-height: 1.2;
      }
      .vca-input {
        width: 100%;
        min-height: 44px;
        padding: 12px 14px;
        background: var(--bg-elevated);
        color: var(--text-primary);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px);
        font-family: var(--font-mono);
        font-size: 0.9375rem;
        line-height: 1.4;
        outline: none;
        transition:
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          background    var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          box-shadow    var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vca-input::placeholder { color: var(--text-ghost); }
      .vca-input:hover  { border-color: var(--border-emphasis); }
      .vca-input:focus,
      .vca-input:focus-visible {
        border-color: var(--accent);
        box-shadow: var(--focus-ring);
      }
      .vca-input.has-error { border-color: var(--danger); }
      .vca-input.has-error:focus,
      .vca-input.has-error:focus-visible {
        box-shadow: 0 0 0 3px color-mix(in srgb, var(--danger) 30%, transparent);
      }
      .vca-input:disabled {
        opacity: 0.55;
        cursor: not-allowed;
      }

      /* Larger, centred input for the code */
      .vca-input-code {
        font-size: 1.5rem;
        font-weight: 600;
        letter-spacing: 0.12em;
        text-align: center;
        text-transform: uppercase;
        padding: 14px 18px;
      }

      .vca-fielderr {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--danger);
        margin: 0;
        line-height: 1.4;
      }
      .vca-hint {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--text-faint);
        margin: 0;
        line-height: 1.5;
      }

      /* ── Device summary card ─────────────────────────────── */
      .vca-summary {
        background: var(--bg-elevated);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px);
        padding: 14px 16px;
        display: flex;
        flex-direction: column;
        gap: 8px;
      }
      .vca-summary-row {
        display: flex;
        align-items: baseline;
        justify-content: space-between;
        gap: 12px;
      }
      .vca-summary-key {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 500;
        letter-spacing: 0.12em;
        text-transform: uppercase;
        color: var(--text-faint);
        flex-shrink: 0;
      }
      .vca-summary-val {
        font-family: var(--font-mono);
        font-size: 0.8125rem;
        color: var(--text-secondary);
        text-align: right;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
      }
      .vca-summary-code {
        letter-spacing: 0.08em;
        font-weight: 600;
        color: var(--text-primary);
      }

      /* ── Inline code snippet ─────────────────────────────── */
      .vca-code-inline {
        font-family: var(--font-mono);
        font-size: 0.875em;
        font-weight: 600;
        letter-spacing: 0.06em;
        color: var(--text-primary);
        background: var(--bg-elevated);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius-sm, 6px);
        padding: 1px 5px;
      }

      /* ── Approve button (the ONE accent moment) ──────────── */
      .vca-btn-approve {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        width: 100%;
        min-height: 48px;
        margin-top: 4px;
        padding: 13px 18px;
        background: var(--accent);
        color: #fff;
        border: 1px solid var(--accent);
        border-radius: var(--radius, 10px);
        font-family: var(--font-mono);
        font-size: 0.9375rem;
        font-weight: 600;
        letter-spacing: 0.01em;
        cursor: pointer;
        transition:
          background   var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          transform    var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          box-shadow   var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vca-btn-approve:hover:not(:disabled) {
        background: var(--accent-hover);
        border-color: var(--accent-hover);
        transform: translateY(-1px);
        box-shadow: 0 6px 18px color-mix(in srgb, var(--accent) 30%, transparent);
      }
      .vca-btn-approve:active:not(:disabled) { transform: translateY(0); box-shadow: none; }
      .vca-btn-approve:focus-visible { outline: none; box-shadow: var(--focus-ring); }
      .vca-btn-approve:disabled {
        opacity: 0.5;
        cursor: not-allowed;
        transform: none;
        box-shadow: none;
      }

      /* ── Deny button (destructive ghost) ─────────────────── */
      .vca-btn-deny {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        width: 100%;
        min-height: 44px;
        padding: 11px 16px;
        background: transparent;
        color: var(--text-faint);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px);
        font-family: var(--font-mono);
        font-size: 0.875rem;
        font-weight: 500;
        letter-spacing: 0.01em;
        cursor: pointer;
        transition:
          color        var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          background   var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vca-btn-deny:hover:not(:disabled) {
        color: var(--danger);
        border-color: color-mix(in srgb, var(--danger) 45%, var(--border-strong));
        background: color-mix(in srgb, var(--danger) 6%, transparent);
      }
      .vca-btn-deny:focus-visible { outline: none; box-shadow: var(--focus-ring); }
      .vca-btn-deny:disabled {
        opacity: 0.4;
        cursor: not-allowed;
      }

      /* ── Ghost button (secondary actions) ───────────────── */
      .vca-btn-ghost {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        padding: 11px 16px;
        min-height: 44px;
        background: transparent;
        color: var(--text-faint);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px);
        font-family: var(--font-mono);
        font-size: 0.875rem;
        font-weight: 500;
        letter-spacing: 0.01em;
        cursor: pointer;
        transition:
          color        var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          background   var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vca-btn-ghost:hover {
        color: var(--text-secondary);
        border-color: var(--border-emphasis);
        background: var(--bg-hover);
      }
      .vca-btn-ghost:focus-visible { outline: none; box-shadow: var(--focus-ring); }

      /* ── Responsive ─────────────────────────────────────── */
      @media (max-width: 480px) {
        .vca-card {
          padding: var(--sp-5, 40px) var(--sp-3, 24px);
          border-radius: var(--radius, 10px);
        }
        .vca-title { font-size: 1.4375rem; }
        .vca-input-code { font-size: 1.25rem; }
      }
      @media (max-width: 360px) {
        .vca-card { padding: var(--sp-4, 32px) 16px; }
      }
    `})}function S(){let{user:e,loading:t}=a(),[n,i]=(0,c.useState)(`entry`),[o,f]=(0,c.useState)(``),p=(0,c.useMemo)(()=>{try{return u(new URL(window.location.href).searchParams.get(`code`)||``)}catch{return``}},[]);if((0,c.useEffect)(()=>{t||e||r(d())},[t,e]),t||!e)return(0,l.jsx)(g,{});function m(e){f(e),i(`success`)}function h(e){f(e),i(`denied`)}function y(){i(`entry`),f(``);try{let e=new URL(window.location.href);e.searchParams.delete(`code`),window.history.replaceState({},``,e.pathname)}catch{}}return(0,l.jsxs)(s,{slim:!0,children:[(0,l.jsx)(x,{}),(0,l.jsxs)(`div`,{className:`vca-wrap`,children:[n===`success`&&(0,l.jsx)(_,{userCode:o,onActivateAnother:y}),n===`denied`&&(0,l.jsx)(v,{userCode:o,onActivateAnother:y}),n===`entry`&&(0,l.jsx)(b,{initialCode:p,onApproved:m,onDenied:h})]})]})}export{S as default};