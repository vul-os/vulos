import{n as e,t}from"./jsx-runtime-Qy3n81sD.js";import{c as n,l as r,m as i,p as a,u as o}from"./index-CKoCcdrv.js";import{i as s}from"./ui-J8a9L6bi.js";var c=e();async function l(){return`/`}function u(e){let t=e.length%4==0?``:`=`.repeat(4-e.length%4),n=e.replace(/-/g,`+`).replace(/_/g,`/`)+t,r=atob(n),i=new Uint8Array(r.length);for(let e=0;e<r.length;e++)i[e]=r.charCodeAt(e);return i}function d(e){let t=e instanceof Uint8Array?e:new Uint8Array(e),n=``;for(let e=0;e<t.length;e++)n+=String.fromCharCode(t[e]);return btoa(n).replace(/\+/g,`-`).replace(/\//g,`_`).replace(/=+$/,``)}function f(e=typeof navigator<`u`?navigator:void 0){return!!(e&&e.credentials&&typeof e.credentials.create==`function`&&typeof e.credentials.get==`function`&&typeof window<`u`&&window.PublicKeyCredential!==void 0)}function p(e){let t=e&&e.publicKey?{...e.publicKey}:{...e};typeof t.challenge==`string`&&(t.challenge=u(t.challenge)),t.user&&typeof t.user.id==`string`&&(t.user={...t.user,id:u(t.user.id)});let n=e=>Array.isArray(e)?e.map(e=>({...e,id:typeof e.id==`string`?u(e.id):e.id})):e;return t.allowCredentials&&=n(t.allowCredentials),t.excludeCredentials&&=n(t.excludeCredentials),t}function m(e){let t=e.response;return{id:e.id,rawId:d(e.rawId),type:e.type,response:{authenticatorData:d(t.authenticatorData),clientDataJSON:d(t.clientDataJSON),signature:d(t.signature),userHandle:t.userHandle?d(t.userHandle):null},clientExtensionResults:typeof e.getClientExtensionResults==`function`?e.getClientExtensionResults():{}}}async function h(e){let t=await e.text();if(!t)return null;try{return JSON.parse(t)}catch{return null}}function g(e,t,n){let r=t&&(t.error||t.message)||(e.status===429?`Too many attempts — try again shortly.`:n),i=Error(r);return i.status=e.status,i}async function _(e,t={}){let n=t.fetch||fetch,r=t.credentials||(typeof navigator<`u`?navigator.credentials:void 0);if(!r)throw Error(`This browser does not support passkeys.`);if(!e)throw Error(`Enter your Vulos address first.`);let i=await n(`/api/auth/webauthn/login/begin`,{method:`POST`,credentials:`include`,headers:{"Content-Type":`application/json`,Accept:`application/json`},body:JSON.stringify({email:e})}),a=await h(i);if(!i.ok)throw g(i,a,`No passkeys registered for this account.`);if(!a||!a.user_id||!a.options)throw Error(`Unexpected passkey challenge from server.`);let o=p(a.options),s=await r.get({publicKey:o});if(!s)throw Error(`Passkey sign-in was cancelled.`);let c=await n(`/api/auth/webauthn/login/finish?user_id=${encodeURIComponent(a.user_id)}`,{method:`POST`,credentials:`include`,headers:{"Content-Type":`application/json`,Accept:`application/json`},body:JSON.stringify(m(s))}),l=await h(c);if(!c.ok)throw g(c,l,`Passkey sign-in failed.`);return l}var v=t(),y=`/api/auth/oauth/providers`;function b({id:e}){return e===`google`?(0,v.jsxs)(`svg`,{width:`16`,height:`16`,viewBox:`0 0 16 16`,"aria-hidden":`true`,children:[(0,v.jsx)(`circle`,{cx:`8`,cy:`8`,r:`6.5`,fill:`none`,stroke:`currentColor`,strokeWidth:`1.4`}),(0,v.jsx)(`path`,{d:`M8 8h4a4 4 0 1 1-1.2-2.9`,fill:`none`,stroke:`currentColor`,strokeWidth:`1.4`,strokeLinecap:`round`})]}):e===`microsoft`?(0,v.jsxs)(`svg`,{width:`16`,height:`16`,viewBox:`0 0 16 16`,"aria-hidden":`true`,children:[(0,v.jsx)(`rect`,{x:`2.5`,y:`2.5`,width:`4.5`,height:`4.5`,fill:`currentColor`}),(0,v.jsx)(`rect`,{x:`9`,y:`2.5`,width:`4.5`,height:`4.5`,fill:`currentColor`,opacity:`0.6`}),(0,v.jsx)(`rect`,{x:`2.5`,y:`9`,width:`4.5`,height:`4.5`,fill:`currentColor`,opacity:`0.6`}),(0,v.jsx)(`rect`,{x:`9`,y:`9`,width:`4.5`,height:`4.5`,fill:`currentColor`})]}):e===`github`?(0,v.jsx)(`svg`,{width:`16`,height:`16`,viewBox:`0 0 16 16`,"aria-hidden":`true`,fill:`currentColor`,children:(0,v.jsx)(`path`,{fillRule:`evenodd`,d:`M8 0.2a8 8 0 0 0-2.5 15.6c.4.07.55-.17.55-.38v-1.34c-2.05.44-2.5-.99-2.5-.99-.34-.86-.83-1.09-.83-1.09-.68-.46.05-.45.05-.45.75.05 1.14.77 1.14.77.67 1.14 1.75.81 2.18.62.07-.48.26-.81.47-1-1.64-.19-3.36-.82-3.36-3.64 0-.8.29-1.46.76-1.98-.08-.19-.33-.94.07-1.96 0 0 .62-.2 2.02.76a7 7 0 0 1 3.68 0c1.4-.96 2.02-.76 2.02-.76.4 1.02.15 1.77.07 1.96.48.52.76 1.18.76 1.98 0 2.83-1.72 3.45-3.36 3.63.27.23.5.68.5 1.37v2.03c0 .21.15.46.55.38A8 8 0 0 0 8 0.2Z`})}):e===`discord`?(0,v.jsx)(`svg`,{width:`16`,height:`16`,viewBox:`0 0 16 16`,"aria-hidden":`true`,fill:`currentColor`,children:(0,v.jsx)(`path`,{d:`M13.2 3.1A11 11 0 0 0 10.5 2.3l-.16.33a10 10 0 0 1 2.4.77 8.6 8.6 0 0 0-6.5 0c.74-.34 1.55-.6 2.4-.77L8.9 2.3a11 11 0 0 0-2.7.8C2.9 6.1 2.35 9.1 2.6 12.05a11 11 0 0 0 3.36 1.7l.4-.66c-.44-.16-.86-.36-1.26-.6.1-.08.2-.16.3-.24a7.9 7.9 0 0 0 6.8 0l.3.24c-.4.24-.83.44-1.27.6l.4.66a11 11 0 0 0 3.37-1.7c.3-3.4-.55-6.37-2.9-8.95ZM6.3 10.2c-.65 0-1.18-.6-1.18-1.33 0-.74.52-1.34 1.18-1.34s1.2.6 1.18 1.34c0 .73-.53 1.33-1.18 1.33Zm3.4 0c-.65 0-1.18-.6-1.18-1.33 0-.74.52-1.34 1.18-1.34s1.2.6 1.18 1.34c0 .73-.52 1.33-1.18 1.33Z`})}):(0,v.jsxs)(`svg`,{width:`16`,height:`16`,viewBox:`0 0 16 16`,"aria-hidden":`true`,children:[(0,v.jsx)(`circle`,{cx:`6`,cy:`8`,r:`3`,fill:`none`,stroke:`currentColor`,strokeWidth:`1.4`}),(0,v.jsx)(`path`,{d:`M8.5 8H14M12 6l2 2-2 2`,fill:`none`,stroke:`currentColor`,strokeWidth:`1.4`,strokeLinecap:`round`,strokeLinejoin:`round`})]})}function x({returnTo:e}){let[t,n]=(0,c.useState)([]);if((0,c.useEffect)(()=>{let e=!0;return(async()=>{try{let t=await fetch(y,{method:`GET`,credentials:`include`,headers:{Accept:`application/json`},cache:`no-store`});if(!t.ok)return;let r=await t.json().catch(()=>null),i=r&&Array.isArray(r.providers)?r.providers:[];e&&n(i)}catch{}})(),()=>{e=!1}},[]),!t.length)return null;let r=i(e&&e.startsWith(`/`)&&!e.startsWith(`//`)?e:`/`),a=e=>`/api/auth/oauth/${encodeURIComponent(e)}/start?return=${encodeURIComponent(r)}`;return(0,v.jsxs)(`div`,{className:`vc-social`,"data-testid":`social-login`,children:[(0,v.jsx)(`style`,{children:`
        .vc-social {
          display: flex;
          flex-direction: column;
          gap: 10px;
        }
        .vc-social-or {
          display: flex;
          align-items: center;
          gap: 12px;
          margin: 2px 0;
          color: var(--text-dim, #8a8f98);
          font-family: var(--font-mono, monospace);
          font-size: 0.75rem;
          text-transform: uppercase;
          letter-spacing: 0.08em;
        }
        .vc-social-or::before,
        .vc-social-or::after {
          content: '';
          flex: 1;
          height: 1px;
          background: var(--border-strong, #2a2a2e);
        }
        .vc-social-btn {
          display: inline-flex;
          align-items: center;
          justify-content: center;
          gap: 10px;
          width: 100%;
          min-height: 46px;
          padding: 12px 18px;
          background: transparent;
          color: var(--text, #e8e8ea);
          border: 1px solid var(--border-strong, #2a2a2e);
          border-radius: var(--radius, 10px);
          font-family: var(--font-mono, monospace);
          font-size: 0.9375rem;
          font-weight: 600;
          letter-spacing: 0.01em;
          text-decoration: none;
          cursor: pointer;
          transition: border-color 160ms, background 160ms, transform 160ms;
        }
        .vc-social-btn:hover {
          border-color: var(--accent, #6366f1);
          background: color-mix(in srgb, var(--accent, #6366f1) 8%, transparent);
          transform: translateY(-1px);
        }
        .vc-social-btn:focus-visible {
          outline: none;
          box-shadow: var(--focus-ring, 0 0 0 3px rgba(99,102,241,0.5));
        }
      `}),(0,v.jsx)(`div`,{className:`vc-social-or`,role:`separator`,"aria-label":`or continue with`,children:(0,v.jsx)(`span`,{children:`or continue with`})}),t.map(e=>(0,v.jsxs)(`a`,{className:`vc-social-btn`,href:a(e.id),"data-provider":e.id,children:[(0,v.jsx)(b,{id:e.id}),(0,v.jsxs)(`span`,{children:[`Continue with `,e.name]})]},e.id))]})}var S={provider_not_configured:`That sign-in provider is not available.`,oauth_state_missing:`Your sign-in session expired. Please try again.`,oauth_state_invalid:`Your sign-in session was invalid. Please try again.`,oauth_state_expired:`Your sign-in session expired. Please try again.`,oauth_state_mismatch:`Your sign-in session could not be verified. Please try again.`,oauth_denied:`Sign-in was cancelled.`,oauth_no_code:`Sign-in did not complete. Please try again.`,oauth_no_email:`That account did not share a verified email, so we can’t sign you in.`,oauth_exchange_failed:`We couldn’t complete sign-in with that provider. Please try again.`,email_unverified:`That provider hasn’t verified this email, so it can’t be linked to your account.`,email_taken:`An account with that email already exists — sign in with your password to link it.`,email_verification_required:`Please verify your email before signing in.`};function C(){try{let e=new URL(window.location.href).searchParams.get(`error`);if(e)return S[e]||`Sign in failed. Please try again.`}catch{}return null}var w=/^[^\s@]+@[^\s@]+\.[^\s@]+$/;function T(e){return e.trim().toLowerCase()}function E(){try{let e=new URL(window.location.href).searchParams.get(`return`);if(e&&e.startsWith(`/`)&&!e.startsWith(`//`))return e}catch{}return null}async function D(e){a(e||await l())}function O(){return(0,v.jsxs)(`svg`,{"aria-hidden":`true`,className:`vc-auth-passkey-key`,width:`17`,height:`17`,viewBox:`0 0 24 24`,fill:`none`,stroke:`currentColor`,strokeWidth:`1.7`,strokeLinecap:`round`,strokeLinejoin:`round`,children:[(0,v.jsx)(`circle`,{cx:`8`,cy:`8`,r:`5`}),(0,v.jsx)(`path`,{d:`M11.5 11.5 L21 21`}),(0,v.jsx)(`path`,{d:`M17.5 17.5 L20 15`}),(0,v.jsx)(`path`,{d:`M15 15 L17.5 12.5`})]})}function k({size:e=14}){return(0,v.jsx)(`span`,{"aria-hidden":`true`,style:{display:`inline-block`,width:e,height:e,borderRadius:`50%`,border:`2px solid color-mix(in srgb, currentColor 25%, transparent)`,borderTopColor:`currentColor`,animation:`vcAuthSpin 0.7s linear infinite`}})}function A(){let{user:e,login:t,refresh:i,loading:a}=r(),[l,u]=(0,c.useState)(``),[d,p]=(0,c.useState)(``),[m,h]=(0,c.useState)(!1),[g,y]=(0,c.useState)({email:!1,password:!1}),[b,S]=(0,c.useState)(!1),[A,M]=(0,c.useState)(()=>C()),[N,P]=(0,c.useState)(!1),F=(0,c.useMemo)(()=>f(),[]),I=(0,c.useMemo)(()=>E(),[]);(0,c.useEffect)(()=>{!a&&e&&D(I)},[a,e,I]);let L=w.test(l.trim().toLowerCase()),R=g.email&&!L?`Enter the email address you signed up with.`:null,z=g.password&&d.length<1?`Enter your password.`:null,B=L&&d.length>0&&!b;async function V(e){if(e.preventDefault(),y({email:!0,password:!0}),B){M(null),S(!0);try{await t(T(l),d),await D(I)}catch(e){M(e&&e.message?e.message:`Sign in failed.`),S(!1)}}}async function H(){if(y(e=>({...e,email:!0})),!L){M(`Enter your email address first, then sign in with your passkey.`);return}M(null),P(!0);try{await _(T(l)),await i(),await D(I)}catch(e){let t=e&&e.name;t!==`AbortError`&&t!==`NotAllowedError`&&M(e&&e.message?e.message:`Passkey sign-in failed.`),P(!1)}}let U=I?`/signup?return=${encodeURIComponent(I)}`:`/signup`;return(0,v.jsxs)(s,{slim:!0,children:[(0,v.jsx)(j,{}),(0,v.jsx)(`div`,{className:`vc-auth-wrap`,children:(0,v.jsxs)(`div`,{className:`vc-auth-card vc-auth-reveal`,children:[(0,v.jsxs)(`div`,{className:`vc-auth-head`,children:[(0,v.jsxs)(`a`,{href:`/`,onClick:e=>{e.preventDefault(),o(`/`)},className:`vc-auth-brand`,"aria-label":`Vulos — home`,children:[(0,v.jsx)(n,{size:36,tone:`on-dark`}),(0,v.jsxs)(`span`,{className:`vc-auth-wordmark`,children:[`vulos`,(0,v.jsx)(`span`,{className:`vc-auth-wordmark-suffix`,children:`.org`})]})]}),(0,v.jsx)(`p`,{className:`vc-auth-eyebrow`,children:`Vulos account`}),(0,v.jsx)(`h1`,{className:`vc-auth-title`,children:`Welcome back`}),(0,v.jsx)(`p`,{className:`vc-auth-subtitle`,children:`One sign-in for your OS, your apps and your cloud console.`})]}),(0,v.jsxs)(`form`,{noValidate:!0,onSubmit:V,className:`vc-auth-form`,children:[A&&(0,v.jsxs)(`div`,{role:`alert`,id:`login-error`,className:`vc-auth-alert`,"aria-live":`assertive`,children:[(0,v.jsx)(`span`,{className:`vc-auth-alert-tag`,children:`Error`}),(0,v.jsx)(`span`,{className:`vc-auth-alert-msg`,children:A})]}),(0,v.jsxs)(`div`,{className:`vc-auth-field`,children:[(0,v.jsx)(`div`,{className:`vc-auth-label-row`,children:(0,v.jsx)(`label`,{htmlFor:`login-email`,className:`vc-auth-label`,children:`Email`})}),(0,v.jsx)(`input`,{id:`login-email`,name:`email`,type:`email`,autoComplete:`email`,inputMode:`email`,spellCheck:!1,autoCapitalize:`none`,value:l,onChange:e=>{u(e.target.value),A&&M(null)},onBlur:()=>y(e=>({...e,email:!0})),placeholder:`you@example.com`,required:!0,"aria-invalid":R?`true`:`false`,"aria-describedby":R?`login-email-err`:void 0,className:`vc-auth-input${R?` has-error`:``}`,autoFocus:!0}),R&&(0,v.jsx)(`p`,{id:`login-email-err`,className:`vc-auth-fielderr`,children:R})]}),(0,v.jsxs)(`div`,{className:`vc-auth-field`,children:[(0,v.jsxs)(`div`,{className:`vc-auth-label-row`,children:[(0,v.jsx)(`label`,{htmlFor:`login-password`,className:`vc-auth-label`,children:`Password`}),(0,v.jsx)(`a`,{href:`/forgot`,onClick:e=>{e.preventDefault(),o(`/forgot`)},className:`vc-auth-aux`,children:`Forgot?`})]}),(0,v.jsxs)(`div`,{className:`vc-auth-input-wrap`,children:[(0,v.jsx)(`input`,{id:`login-password`,name:`password`,type:m?`text`:`password`,autoComplete:`current-password`,value:d,onChange:e=>{p(e.target.value),A&&M(null)},onBlur:()=>y(e=>({...e,password:!0})),placeholder:`Your password`,required:!0,"aria-invalid":z?`true`:`false`,"aria-describedby":z?`login-password-err`:void 0,className:`vc-auth-input has-trailing${z?` has-error`:``}`}),(0,v.jsx)(`button`,{type:`button`,onClick:()=>h(e=>!e),className:`vc-auth-eye`,"aria-label":m?`Hide password`:`Show password`,"aria-pressed":m,children:m?`Hide`:`Show`})]}),z&&(0,v.jsx)(`p`,{id:`login-password-err`,className:`vc-auth-fielderr`,children:z})]}),(0,v.jsx)(`button`,{type:`submit`,className:`vc-auth-submit`,disabled:!B,"aria-busy":b?`true`:`false`,children:b?(0,v.jsxs)(v.Fragment,{children:[(0,v.jsx)(k,{size:14}),(0,v.jsx)(`span`,{children:`Signing in…`})]}):`Sign in`}),F&&(0,v.jsxs)(v.Fragment,{children:[(0,v.jsx)(`div`,{className:`vc-auth-or`,role:`separator`,"aria-label":`or`,children:(0,v.jsx)(`span`,{children:`or`})}),(0,v.jsx)(`button`,{type:`button`,className:`vc-auth-passkey`,onClick:H,disabled:N||b,"aria-busy":N?`true`:`false`,children:N?(0,v.jsxs)(v.Fragment,{children:[(0,v.jsx)(k,{size:14}),(0,v.jsx)(`span`,{children:`Waiting for passkey…`})]}):(0,v.jsxs)(v.Fragment,{children:[(0,v.jsx)(O,{}),(0,v.jsx)(`span`,{children:`Sign in with a passkey`})]})})]}),(0,v.jsx)(x,{returnTo:I}),(0,v.jsx)(`div`,{className:`vc-auth-foot`,children:(0,v.jsxs)(`span`,{className:`vc-auth-foot-text`,children:[`Don't have an account?`,` `,(0,v.jsx)(`a`,{href:U,onClick:e=>{e.preventDefault(),o(U)},className:`vc-auth-foot-link`,children:`Sign up`})]})})]})]})})]})}function j(){return(0,v.jsx)(`style`,{children:`
      @keyframes vcAuthSpin { to { transform: rotate(360deg); } }
      @keyframes vcAuthRise {
        from { opacity: 0; transform: translateY(8px); }
        to   { opacity: 1; transform: translateY(0); }
      }

      .vc-auth-wrap {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 100%;
      }

      .vc-auth-card {
        width: 100%;
        max-width: 420px;
        background: var(--bg-surface);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius-lg, 16px);
        padding: var(--sp-6, 48px) var(--sp-5, 40px);
        box-shadow: var(--shadow);
      }

      .vc-auth-reveal {
        animation: vcAuthRise 280ms var(--ease, cubic-bezier(.22,1,.36,1)) both;
      }
      @media (prefers-reduced-motion: reduce) {
        .vc-auth-reveal { animation: none; }
      }

      .vc-auth-head {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 6px;
        margin-bottom: var(--sp-4, 32px);
      }
      .vc-auth-brand {
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
      .vc-auth-brand:hover { opacity: 0.86; }
      .vc-auth-brand:focus-visible {
        outline: none;
        box-shadow: var(--focus-ring);
      }
      .vc-auth-wordmark {
        font-family: var(--font-mono);
        font-weight: 600;
        font-size: 0.9375rem;
        letter-spacing: -0.01em;
        color: var(--text-primary);
        line-height: 1;
      }
      .vc-auth-wordmark-suffix {
        color: var(--text-faint);
        font-weight: 400;
      }
      .vc-auth-eyebrow {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 500;
        letter-spacing: 0.16em;
        text-transform: uppercase;
        color: var(--text-faint);
        margin: 0;
      }
      .vc-auth-title {
        font-family: var(--font-mono);
        font-size: 1.625rem;
        font-weight: 700;
        letter-spacing: -0.025em;
        color: var(--text-primary);
        line-height: 1.15;
        margin: 4px 0 0;
      }
      .vc-auth-subtitle {
        font-family: var(--font-sans);
        font-size: 0.9375rem;
        color: var(--text-tertiary);
        line-height: 1.55;
        margin: 8px 0 0;
        max-width: none;
      }

      .vc-auth-form {
        display: flex;
        flex-direction: column;
        gap: 16px;
      }

      /* ── Alert (above-form error region) ────────────────── */
      .vc-auth-alert {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        padding: 12px 14px;
        border-radius: var(--radius, 10px);
        background: color-mix(in srgb, var(--warn) 10%, var(--bg-elevated));
        border: 1px solid color-mix(in srgb, var(--warn) 35%, var(--border-emphasis));
      }
      .vc-auth-alert-tag {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 600;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--warn);
        padding-top: 1px;
        flex-shrink: 0;
      }
      .vc-auth-alert-msg {
        font-family: var(--font-sans);
        font-size: 0.875rem;
        color: var(--text-secondary);
        line-height: 1.5;
      }

      /* ── Field ─────────────────────────────────────────── */
      .vc-auth-field {
        display: flex;
        flex-direction: column;
        gap: 6px;
      }
      .vc-auth-label-row {
        display: flex;
        align-items: baseline;
        justify-content: space-between;
        gap: 12px;
      }
      .vc-auth-label {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        font-weight: 500;
        letter-spacing: 0.06em;
        text-transform: uppercase;
        color: var(--text-tertiary);
        line-height: 1.2;
      }
      .vc-auth-aux {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--text-faint);
        text-decoration: none;
        transition: color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-aux:hover { color: var(--accent); }
      .vc-auth-aux:focus-visible {
        outline: none;
        box-shadow: var(--focus-ring);
        border-radius: var(--radius-sm, 6px);
      }

      .vc-auth-input-wrap {
        position: relative;
        display: flex;
        align-items: center;
      }

      .vc-auth-input {
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
        letter-spacing: 0;
        outline: none;
        transition:
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          background var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          box-shadow var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-input::placeholder { color: var(--text-ghost); }
      .vc-auth-input:hover {
        border-color: var(--border-emphasis);
      }
      .vc-auth-input:focus,
      .vc-auth-input:focus-visible {
        border-color: var(--accent);
        background: var(--bg-elevated);
        box-shadow: var(--focus-ring);
      }
      .vc-auth-input.has-trailing { padding-right: 64px; }
      .vc-auth-input.has-error {
        border-color: var(--danger);
      }
      .vc-auth-input.has-error:focus,
      .vc-auth-input.has-error:focus-visible {
        box-shadow: 0 0 0 3px color-mix(in srgb, var(--danger) 30%, transparent);
      }

      .vc-auth-eye {
        position: absolute;
        right: 4px;
        top: 50%;
        transform: translateY(-50%);
        min-height: 36px;
        padding: 6px 10px;
        background: transparent;
        border: 1px solid transparent;
        border-radius: var(--radius-sm, 6px);
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 500;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--text-faint);
        cursor: pointer;
        transition:
          color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          background var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-eye:hover { color: var(--text-secondary); background: rgba(255,255,255,0.04); }
      .vc-auth-eye:focus-visible {
        outline: none;
        color: var(--text-primary);
        box-shadow: var(--focus-ring);
      }

      .vc-auth-fielderr {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--danger);
        margin: 0;
        line-height: 1.4;
      }

      .vc-auth-hint {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--text-faint);
        margin: 0;
        line-height: 1.5;
      }

      /* ── Submit (the one accent moment) ─────────────────── */
      .vc-auth-submit {
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
          background var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          transform var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          box-shadow var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-submit:hover:not(:disabled) {
        background: var(--accent-hover);
        border-color: var(--accent-hover);
        transform: translateY(-1px);
        box-shadow: 0 6px 18px color-mix(in srgb, var(--accent) 30%, transparent);
      }
      .vc-auth-submit:active:not(:disabled) {
        transform: translateY(0);
        box-shadow: none;
      }
      .vc-auth-submit:focus-visible {
        outline: none;
        box-shadow: var(--focus-ring);
      }
      .vc-auth-submit:disabled {
        opacity: 0.5;
        cursor: not-allowed;
        transform: none;
        box-shadow: none;
      }

      /* ── Passkey (secondary auth path) ─────────────────── */
      .vc-auth-or {
        display: flex;
        align-items: center;
        gap: 12px;
        margin: 4px 0;
        color: var(--text-dim, #8a8f98);
        font-family: var(--font-mono);
        font-size: 0.75rem;
        text-transform: uppercase;
        letter-spacing: 0.08em;
      }
      .vc-auth-or::before,
      .vc-auth-or::after {
        content: '';
        flex: 1;
        height: 1px;
        background: var(--border-strong);
      }
      .vc-auth-passkey {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        width: 100%;
        min-height: 48px;
        padding: 13px 18px;
        background: transparent;
        color: var(--text, #e8e8ea);
        border: 1px solid var(--border-strong);
        border-radius: var(--radius, 10px);
        font-family: var(--font-mono);
        font-size: 0.9375rem;
        font-weight: 600;
        letter-spacing: 0.01em;
        cursor: pointer;
        transition:
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          background var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          transform var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-passkey:hover:not(:disabled) {
        border-color: var(--accent);
        background: color-mix(in srgb, var(--accent) 8%, transparent);
        transform: translateY(-1px);
      }
      .vc-auth-passkey:focus-visible {
        outline: none;
        box-shadow: var(--focus-ring);
      }
      .vc-auth-passkey:disabled {
        opacity: 0.5;
        cursor: not-allowed;
        transform: none;
      }
      .vc-auth-passkey-key {
        font-size: 1.1rem;
        line-height: 1;
      }

      /* ── Foot ──────────────────────────────────────────── */
      .vc-auth-foot {
        display: flex;
        justify-content: center;
        margin-top: 18px;
      }
      .vc-auth-foot-text {
        font-family: var(--font-mono);
        font-size: 0.8125rem;
        color: var(--text-tertiary);
      }
      .vc-auth-foot-link {
        color: var(--text-primary);
        font-weight: 500;
        text-decoration: none;
        border-bottom: 1px solid var(--border-emphasis);
        transition:
          color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1)),
          border-color var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-foot-link:hover {
        color: var(--accent);
        border-bottom-color: var(--accent);
      }
      .vc-auth-foot-link:focus-visible {
        outline: none;
        box-shadow: var(--focus-ring);
        border-radius: var(--radius-sm, 6px);
      }

      /* ── Inline ok / flash banner (used by Forgot/Reset) ── */
      .vc-auth-ok {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        padding: 14px 16px;
        border-radius: var(--radius, 10px);
        background: color-mix(in srgb, var(--good) 8%, var(--bg-elevated));
        border: 1px solid color-mix(in srgb, var(--good) 30%, var(--border-emphasis));
      }
      .vc-auth-ok-tag {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        font-weight: 600;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--good);
        padding-top: 1px;
        flex-shrink: 0;
      }
      .vc-auth-ok-msg {
        font-family: var(--font-sans);
        font-size: 0.875rem;
        color: var(--text-secondary);
        line-height: 1.55;
      }

      /* ── Password strength meter ───────────────────────── */
      .vc-auth-meter {
        display: grid;
        grid-template-columns: repeat(4, 1fr);
        gap: 4px;
        margin-top: 2px;
      }
      .vc-auth-meter-seg {
        height: 3px;
        border-radius: 99px;
        background: var(--border-strong);
        transition: background var(--dur-fast, 160ms) var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vc-auth-meter-seg.lit-weak   { background: var(--danger); }
      .vc-auth-meter-seg.lit-okay   { background: var(--warn); }
      .vc-auth-meter-seg.lit-good   { background: var(--accent); }
      .vc-auth-meter-seg.lit-strong { background: var(--good); }
      .vc-auth-meter-label {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        margin-top: 6px;
        line-height: 1.2;
      }
      .vc-auth-meter-label.weak   { color: var(--danger); }
      .vc-auth-meter-label.okay   { color: var(--warn); }
      .vc-auth-meter-label.good   { color: var(--accent); }
      .vc-auth-meter-label.strong { color: var(--good); }

      /* ── Responsive ────────────────────────────────────── */
      @media (max-width: 480px) {
        .vc-auth-card {
          padding: var(--sp-5, 40px) var(--sp-3, 24px);
          border-radius: var(--radius, 10px);
        }
        .vc-auth-title { font-size: 1.4375rem; }
      }
      @media (max-width: 360px) {
        .vc-auth-card {
          padding: var(--sp-4, 32px) 16px;
        }
      }
    `})}export{j as AuthFormStyles,A as default,l as n,x as t};