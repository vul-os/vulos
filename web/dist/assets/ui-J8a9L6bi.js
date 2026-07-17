import{n as e,t}from"./jsx-runtime-Qy3n81sD.js";var n=e(),r=t();function i({children:e,as:t=`section`,slim:n=!1,style:i,className:a,...o}){return(0,r.jsxs)(r.Fragment,{children:[(0,r.jsx)(`style`,{children:`
        .vk-section-inner { max-width: var(--maxw, 1180px); margin: 0 auto; width: 100%; }
        .vk-section {
          width: 100%;
          padding: 96px var(--sp-4, 32px);
        }
        .vk-section.slim { padding: 64px var(--sp-4, 32px); }
        @media (max-width: 1024px) {
          .vk-section      { padding: 80px var(--sp-4, 32px); }
          .vk-section.slim { padding: 56px var(--sp-4, 32px); }
        }
        @media (max-width: 768px) {
          .vk-section      { padding: 64px var(--sp-3, 24px); }
          .vk-section.slim { padding: 48px var(--sp-3, 24px); }
        }
        @media (max-width: 480px) {
          .vk-section      { padding: 48px var(--sp-2, 16px); }
          .vk-section.slim { padding: 40px var(--sp-2, 16px); }
        }
        @media (max-width: 360px) {
          .vk-section      { padding: 40px 12px; }
          .vk-section.slim { padding: 32px 12px; }
        }
      `}),(0,r.jsx)(t,{className:`vk-section${n?` slim`:``}${a?` `+a:``}`,style:i,...o,children:(0,r.jsx)(`div`,{className:`vk-section-inner`,children:e})})]})}function a({children:e,variant:t=`primary`,size:n=`md`,href:i,style:a,className:o,...s}){let c=`vk-btn vk-btn--${t} vk-btn--${n}${o?` `+o:``}`,l=(0,r.jsx)(`style`,{children:`
      .vk-btn {
        display: inline-flex; align-items: center; justify-content: center; gap: 8px;
        font-family: var(--mono, ui-monospace, 'SF Mono', monospace);
        font-size: 0.875rem; font-weight: 500; line-height: 1; cursor: pointer;
        border: 1px solid transparent; text-decoration: none; white-space: nowrap;
        position: relative; letter-spacing: 0.01em;
        transition: background 160ms var(--ease, cubic-bezier(.22,1,.36,1)),
          border-color 160ms var(--ease, cubic-bezier(.22,1,.36,1)),
          color 160ms var(--ease, cubic-bezier(.22,1,.36,1)),
          transform 160ms var(--ease, cubic-bezier(.22,1,.36,1)),
          box-shadow 160ms var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vk-btn:disabled { opacity: 0.5; cursor: default; }
      .vk-btn:focus-visible { outline: none; box-shadow: var(--focus-ring, 0 0 0 3px rgba(15,106,108,.5)); }
      .vk-btn--sm { padding: 8px 16px;  border-radius: var(--radius, 12px); min-height: 36px; }
      .vk-btn--md { padding: 11px 22px; border-radius: var(--radius, 12px); min-height: 44px; }
      .vk-btn--lg { padding: 13px 28px; border-radius: var(--radius-lg, 20px); min-height: 52px; }
      .vk-btn--primary { background: var(--accent, #0f6a6c); color: #fff; border-color: var(--accent, #0f6a6c); }
      .vk-btn--primary:hover:not(:disabled), .vk-btn--primary:focus-visible {
        background: var(--accent-hover, color-mix(in srgb, var(--accent, #0f6a6c) 80%, black));
        border-color: var(--accent-hover, color-mix(in srgb, var(--accent, #0f6a6c) 80%, black));
        transform: translateY(-1px); box-shadow: 0 4px 16px rgba(15,106,108,.28), 0 1px 4px rgba(0,0,0,.3);
      }
      .vk-btn--primary:active { transform: translateY(0); box-shadow: none; }
      .vk-btn--ghost { background: transparent; color: var(--text-dim, #8e95b0); border-color: var(--border-strong, #2e3348); }
      .vk-btn--ghost:hover:not(:disabled), .vk-btn--ghost:focus-visible {
        color: var(--text, #eceef5); border-color: var(--border-emphasis, #333);
        background: var(--hover-overlay); transform: translateY(-1px);
      }
      .vk-btn--ghost:active { transform: translateY(0); }
    `});return i?(0,r.jsxs)(r.Fragment,{children:[l,(0,r.jsx)(`a`,{href:i,className:c,style:a,...s,children:e})]}):(0,r.jsxs)(r.Fragment,{children:[l,(0,r.jsx)(`button`,{className:c,style:a,...s,children:e})]})}function o({children:e,elevated:t=!1,hover:i=!0,style:a,className:o,...s}){let[c,l]=(0,n.useState)(!1);return(0,r.jsxs)(r.Fragment,{children:[(0,r.jsx)(`style`,{children:`
        .vk-card {
          background: var(--bg-elev, #0e1018);
          border: 1px solid var(--border-strong, #2e3348);
          border-radius: var(--radius-lg, 20px);
          padding: var(--sp-4, 32px);
          transition: border-color var(--dur-fast, 120ms) var(--ease, cubic-bezier(.22,1,.36,1)),
            transform var(--dur-fast, 120ms) var(--ease, cubic-bezier(.22,1,.36,1)),
            box-shadow var(--dur-fast, 120ms) var(--ease, cubic-bezier(.22,1,.36,1));
        }
        .vk-card.elevated { background: var(--surface, #151720); }
        .vk-card.lift-hover:hover { border-color: var(--border-emphasis, #333); transform: translateY(-1px); box-shadow: var(--shadow); }
      `}),(0,r.jsx)(`div`,{className:`vk-card${t?` elevated`:``}${i?` lift-hover`:``}${o?` `+o:``}`,style:a,onMouseEnter:i?()=>l(!0):void 0,onMouseLeave:i?()=>l(!1):void 0,"data-hovered":c?``:void 0,...s,children:e})]})}function s({children:e,color:t,dot:n=!1,style:i,...a}){let o={accent:{bg:`color-mix(in srgb, var(--accent) 10%, transparent)`,color:`var(--accent)`,border:`color-mix(in srgb, var(--accent) 24%, transparent)`},good:{bg:`color-mix(in srgb, var(--good) 9%, transparent)`,color:`var(--good)`,border:`color-mix(in srgb, var(--good) 24%, transparent)`},warn:{bg:`color-mix(in srgb, var(--warn) 9%, transparent)`,color:`var(--warn)`,border:`color-mix(in srgb, var(--warn) 26%, transparent)`},danger:{bg:`color-mix(in srgb, var(--danger) 9%, transparent)`,color:`var(--danger)`,border:`color-mix(in srgb, var(--danger) 28%, transparent)`},faint:{bg:`transparent`,color:`var(--text-faint)`,border:`var(--border-strong)`}},s=o[t]??o.faint;return(0,r.jsxs)(`span`,{style:{display:`inline-flex`,alignItems:`center`,gap:`5px`,padding:`4px 10px`,borderRadius:`99px`,fontSize:`0.75rem`,fontFamily:`var(--mono, ui-monospace, monospace)`,fontWeight:500,letterSpacing:`0.04em`,background:s.bg,color:s.color,border:`1px solid ${s.border}`,lineHeight:1.4,...i},...a,children:[n&&(0,r.jsx)(`span`,{"aria-hidden":`true`,style:{display:`inline-block`,width:`5px`,height:`5px`,borderRadius:`50%`,background:`currentColor`,flexShrink:0,opacity:.85}}),e]})}function c({label:e,value:t,sublabel:n,style:i,...a}){return(0,r.jsxs)(`div`,{style:{display:`flex`,flexDirection:`column`,gap:`6px`,...i},...a,children:[(0,r.jsx)(`div`,{style:{fontSize:`clamp(1.5rem, 3vw, 2rem)`,fontFamily:`var(--mono, ui-monospace, monospace)`,fontWeight:700,letterSpacing:`-0.03em`,lineHeight:1,color:`var(--text-primary)`,fontVariantNumeric:`tabular-nums`},children:t}),(0,r.jsx)(`div`,{style:{fontSize:`0.8125rem`,fontFamily:`var(--mono, ui-monospace, monospace)`,color:`var(--text-dim)`,fontWeight:400,letterSpacing:`0.01em`},children:e}),n&&(0,r.jsx)(`div`,{style:{fontSize:`0.75rem`,color:`var(--text-faint)`,lineHeight:1.5},children:n})]})}export{c as a,i,o as n,s as r,a as t};