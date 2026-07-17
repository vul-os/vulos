/**
 * ui/index.jsx — minimal design-system shim for the ported auth pages.
 *
 * The vulos-cloud auth pages import <Section> (a max-width content wrapper with
 * vertical-rhythm padding) from the marketing site's UI kit. The management
 * console doesn't ship that full kit, so this shim provides ONLY the pieces the
 * ported auth flow actually uses — Section + NAV_HEIGHT — with the exact styles
 * copied verbatim from vulos-cloud so the auth surface looks identical to
 * vulos.org. (The console shell itself is styled by app.css, not this file.)
 */

export const NAV_HEIGHT = 56 // px — retained for call-site parity

export function Section({ children, as: Tag = 'section', slim = false, style, className, ...rest }) {
  return (
    <>
      <style>{`
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
      `}</style>
      <Tag
        className={`vk-section${slim ? ' slim' : ''}${className ? ' ' + className : ''}`}
        style={style}
        {...rest}
      >
        <div className="vk-section-inner">{children}</div>
      </Tag>
    </>
  )
}
