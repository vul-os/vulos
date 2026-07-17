import{n as e,t}from"./jsx-runtime-Qy3n81sD.js";import{c as n,u as r}from"./index-BF9biyeb.js";import{i}from"./ui-J8a9L6bi.js";var a=e(),o=t(),s=/^[^\s@]+@[^\s@]+\.[^\s@]+$/;function c({size:e=14}){return(0,o.jsx)(`span`,{"aria-hidden":`true`,style:{display:`inline-block`,width:e,height:e,borderRadius:`50%`,border:`2px solid color-mix(in srgb, currentColor 25%, transparent)`,borderTopColor:`currentColor`,animation:`vcAuthSpin 0.7s linear infinite`}})}var l=`
  @keyframes vcAuthSpin { to { transform: rotate(360deg); } }

  .vc-ar-wrap {
    min-height: 100dvh;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 2rem 1rem;
    background: var(--bg);
  }

  .vc-ar-card {
    width: 100%;
    max-width: 420px;
    background: var(--surface, #0e1117);
    border: 1px solid var(--border, #1e2430);
    border-radius: 10px;
    padding: 2.5rem 2rem;
    display: flex;
    flex-direction: column;
    gap: 1.5rem;
  }

  .vc-ar-header {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.75rem;
    text-align: center;
  }

  .vc-ar-title {
    font-size: 1.2rem;
    font-weight: 600;
    color: var(--text);
    margin: 0;
  }

  .vc-ar-subtitle {
    font-size: 0.85rem;
    color: var(--text-faint, #6b7280);
    margin: 0;
    line-height: 1.5;
  }

  .vc-ar-notice {
    background: color-mix(in srgb, var(--warning, #f59e0b) 8%, transparent);
    border: 1px solid color-mix(in srgb, var(--warning, #f59e0b) 30%, transparent);
    border-radius: 7px;
    padding: 0.85rem 1rem;
    font-size: 0.8rem;
    color: var(--text-faint);
    line-height: 1.55;
  }

  .vc-ar-notice strong {
    color: var(--warning, #f59e0b);
  }

  .vc-ar-form {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  .vc-ar-field {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }

  .vc-ar-label {
    font-size: 0.78rem;
    font-weight: 500;
    color: var(--text-faint);
    letter-spacing: 0.01em;
  }

  .vc-ar-optional {
    font-size: 0.7rem;
    color: var(--text-faint, #6b7280);
    opacity: 0.6;
    margin-left: 0.25rem;
  }

  .vc-ar-input {
    width: 100%;
    box-sizing: border-box;
    background: var(--bg, #060a0f);
    border: 1px solid var(--border, #1e2430);
    border-radius: 6px;
    color: var(--text);
    font-size: 0.9rem;
    font-family: var(--font);
    padding: 0.55rem 0.75rem;
    outline: none;
    transition: border-color 0.15s;
  }

  .vc-ar-input:focus {
    border-color: var(--accent, #3b82f6);
  }

  .vc-ar-input[aria-invalid='true'] {
    border-color: var(--error, #ef4444);
  }

  .vc-ar-field-error {
    font-size: 0.75rem;
    color: var(--error, #ef4444);
  }

  .vc-ar-hint {
    font-size: 0.75rem;
    color: var(--text-faint);
    opacity: 0.7;
    line-height: 1.45;
  }

  .vc-ar-submit {
    width: 100%;
    background: var(--accent, #3b82f6);
    color: #fff;
    border: none;
    border-radius: 6px;
    font-size: 0.9rem;
    font-weight: 600;
    font-family: var(--font);
    padding: 0.65rem 1rem;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    transition: opacity 0.15s;
    margin-top: 0.25rem;
  }

  .vc-ar-submit:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .vc-ar-error-box {
    background: color-mix(in srgb, var(--error, #ef4444) 8%, transparent);
    border: 1px solid color-mix(in srgb, var(--error, #ef4444) 25%, transparent);
    border-radius: 7px;
    padding: 0.75rem 1rem;
    font-size: 0.82rem;
    color: var(--error, #ef4444);
  }

  .vc-ar-footer {
    text-align: center;
    font-size: 0.8rem;
    color: var(--text-faint);
  }

  .vc-ar-footer a {
    color: var(--accent, #3b82f6);
    text-decoration: none;
  }

  .vc-ar-footer a:hover { text-decoration: underline; }

  .vc-ar-success {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    text-align: center;
  }

  .vc-ar-success-icon {
    font-size: 2.5rem;
    line-height: 1;
  }

  .vc-ar-success-title {
    font-size: 1.1rem;
    font-weight: 600;
    color: var(--text);
    margin: 0;
  }

  .vc-ar-success-body {
    font-size: 0.85rem;
    color: var(--text-faint);
    line-height: 1.6;
    margin: 0;
  }

  .vc-ar-back-btn {
    background: transparent;
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text-faint);
    font-size: 0.85rem;
    font-family: var(--font);
    padding: 0.55rem 1rem;
    cursor: pointer;
    transition: border-color 0.15s;
  }

  .vc-ar-back-btn:hover { border-color: var(--accent); color: var(--accent); }
`;function u(){let[e,t]=(0,a.useState)(``),[u,d]=(0,a.useState)(``),[f,p]=(0,a.useState)(``),[m,h]=(0,a.useState)({}),[g,_]=(0,a.useState)(!1),[v,y]=(0,a.useState)(!1),[b,x]=(0,a.useState)(``);function S(e){h(t=>({...t,[e]:!0}))}let C=m.email&&!s.test(e.trim())?`Enter a valid email address.`:null,w=s.test(e.trim())&&f.trim().length>=6&&!g;async function T(t){if(t.preventDefault(),h({email:!0,reviewToken:!0}),w){_(!0),x(``);try{let t=await fetch(`/api/auth/recovery/submit`,{method:`POST`,headers:{"Content-Type":`application/json`},body:JSON.stringify({email:e.trim().toLowerCase(),phone_last4:u.trim()||void 0,review_token:f.trim()})});if(t.status===429){x(`Too many recovery attempts. Your account has been frozen for security review. Contact support at security@vulos.org.`);return}if(!t.ok){let e=await t.json().catch(()=>({}));x(e.error||`Something went wrong. Please try again or contact support.`);return}y(!0)}catch{x(`Network error. Please check your connection and try again.`)}finally{_(!1)}}}return(0,o.jsxs)(o.Fragment,{children:[(0,o.jsx)(`style`,{children:l}),(0,o.jsx)(i,{children:(0,o.jsx)(`div`,{className:`vc-ar-wrap`,children:(0,o.jsxs)(`div`,{className:`vc-ar-card`,children:[(0,o.jsxs)(`div`,{className:`vc-ar-header`,children:[(0,o.jsx)(n,{size:32}),(0,o.jsx)(`h1`,{className:`vc-ar-title`,children:`Account Recovery`}),(0,o.jsx)(`p`,{className:`vc-ar-subtitle`,children:`For users who have lost access to both their 2FA device and all recovery codes.`})]}),v?(0,o.jsxs)(`div`,{className:`vc-ar-success`,role:`status`,children:[(0,o.jsx)(`div`,{className:`vc-ar-success-icon`,"aria-hidden":`true`,children:`✓`}),(0,o.jsx)(`h2`,{className:`vc-ar-success-title`,children:`Recovery request submitted`}),(0,o.jsxs)(`p`,{className:`vc-ar-success-body`,children:[`A confirmation email has been sent to `,(0,o.jsx)(`strong`,{children:e}),`.`,(0,o.jsx)(`br`,{}),(0,o.jsx)(`br`,{}),(0,o.jsx)(`strong`,{children:`A mandatory 14-day security review period now begins.`}),` `,`During this time you will receive notification emails and can cancel the request from any active session. After 14 days and admin review your TOTP will be reset and new recovery codes will be issued.`,(0,o.jsx)(`br`,{}),(0,o.jsx)(`br`,{}),`If you did not initiate this request, sign in immediately and cancel it, or contact `,(0,o.jsx)(`a`,{href:`mailto:security@vulos.org`,children:`security@vulos.org`}),`.`]}),(0,o.jsx)(`button`,{className:`vc-ar-back-btn`,onClick:()=>r(`/login`),children:`Back to login`})]}):(0,o.jsxs)(o.Fragment,{children:[(0,o.jsxs)(`div`,{className:`vc-ar-notice`,role:`note`,children:[(0,o.jsx)(`strong`,{children:`14-day mandatory wait.`}),` `,`After submitting, a 14-day security review period begins before any account changes take effect. This delay is non-negotiable and protects your account from takeover attacks. You will receive email notifications throughout.`]}),b&&(0,o.jsx)(`div`,{className:`vc-ar-error-box`,role:`alert`,children:b}),(0,o.jsxs)(`form`,{className:`vc-ar-form`,onSubmit:T,noValidate:!0,children:[(0,o.jsxs)(`div`,{className:`vc-ar-field`,children:[(0,o.jsx)(`label`,{htmlFor:`ar-email`,className:`vc-ar-label`,children:`Account email`}),(0,o.jsx)(`input`,{id:`ar-email`,type:`email`,className:`vc-ar-input`,autoComplete:`email`,value:e,onChange:e=>t(e.target.value),onBlur:()=>S(`email`),"aria-invalid":C?`true`:void 0,"aria-describedby":C?`ar-email-error`:void 0,placeholder:`you@example.com`,required:!0}),C&&(0,o.jsx)(`span`,{id:`ar-email-error`,className:`vc-ar-field-error`,role:`alert`,children:C})]}),(0,o.jsxs)(`div`,{className:`vc-ar-field`,children:[(0,o.jsxs)(`label`,{htmlFor:`ar-phone`,className:`vc-ar-label`,children:[`Last 4 digits of phone on file`,(0,o.jsx)(`span`,{className:`vc-ar-optional`,children:`(optional)`})]}),(0,o.jsx)(`input`,{id:`ar-phone`,type:`text`,inputMode:`numeric`,maxLength:4,pattern:`\\d{4}`,className:`vc-ar-input`,autoComplete:`tel`,value:u,onChange:e=>d(e.target.value.replace(/\D/g,``).slice(0,4)),placeholder:`1234`}),(0,o.jsx)(`span`,{className:`vc-ar-hint`,children:`Providing this helps our team verify your identity faster.`})]}),(0,o.jsxs)(`div`,{className:`vc-ar-field`,children:[(0,o.jsx)(`label`,{htmlFor:`ar-token`,className:`vc-ar-label`,children:`Manual review token`}),(0,o.jsx)(`input`,{id:`ar-token`,type:`text`,className:`vc-ar-input`,autoComplete:`off`,spellCheck:!1,value:f,onChange:e=>p(e.target.value),onBlur:()=>S(`reviewToken`),placeholder:`Provided by Vulos support`,required:!0}),(0,o.jsxs)(`span`,{className:`vc-ar-hint`,children:[`Contact`,` `,(0,o.jsx)(`a`,{href:`mailto:security@vulos.org`,style:{color:`var(--accent)`},children:`security@vulos.org`}),` `,`to obtain a review token. This is not automated — a team member will verify your identity before issuing one.`]})]}),(0,o.jsx)(`button`,{type:`submit`,className:`vc-ar-submit`,disabled:!w,"aria-busy":g,children:g?(0,o.jsxs)(o.Fragment,{children:[(0,o.jsx)(c,{}),`Submitting…`]}):`Submit recovery request`})]}),(0,o.jsxs)(`div`,{className:`vc-ar-footer`,children:[(0,o.jsx)(`a`,{href:`/login`,onClick:e=>{e.preventDefault(),r(`/login`)},children:`Back to login`}),` · `,(0,o.jsx)(`a`,{href:`/forgot`,onClick:e=>{e.preventDefault(),r(`/forgot`)},children:`Forgot password instead?`})]})]})]})})})]})}export{u as default};