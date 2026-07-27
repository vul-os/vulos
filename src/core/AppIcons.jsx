import { useState } from 'react'

// ─────────────────────────────────────────────────────────────────────────
// VULOS ICONOGRAPHY — one coherent system, not a rainbow icon pack.
//
// Every builtin glyph is drawn on a single 24×24 grid with ONE stroke weight
// (1.7), rounded joins/caps, and NO baked-in colour: shapes use `currentColor`
// so the icon inherits whatever text token the surface sets (dock, launchpad,
// titlebar, App Hub) and themes automatically in light + dark. Tinting happens
// on *state* (hover), never per-app — that's what makes it read as an OS
// rather than a cheap icon pack.
//
// The export API is intentionally stable: `AppIcon` (inline glyph), the
// `AppIconTile` tile, and the `APP_LOGOS` / `APP_COLORS` / `APP_LETTERS` maps
// are all consumed elsewhere (Dock, Window, MissionControl, MobileStack,
// Launchpad, App Hub) — do not rename or remove them.
// ─────────────────────────────────────────────────────────────────────────

// Logo URLs for installed apps (shared with AppHub).
//
// SOVEREIGNTY / LICENCE: these must be SAME-ORIGIN only. Runtime-hotlinking
// third-party logos (previously from upload.wikimedia.org and githubusercontent)
// was three problems at once: (1) many of those logo files are non-free /
// trademarked and not licensed for redistribution by a commercial OS; (2) it
// leaked a signal to those hosts on every launcher render — the opposite of what
// a sovereign OS should do; (3) it broke offline/air-gapped installs. So we only
// reference logos we actually ship under public/icons/. Apps without a bundled
// logo fall through to the locally-installed system icon (/api/desktop/icon/<id>,
// read from the Debian icon theme) and then to a generated letter tile — see
// AppIconTile below.
//
// Do NOT add a third-party logo here without shipping the file under
// public/icons/ AND confirming (with a lawyer, for trademarked marks) that we
// may redistribute it. A generic icon is the safe default.
// eslint-disable-next-line react-refresh/only-export-components
export const APP_LOGOS = {
  // Classic black terminal tile (near-black squircle + light prompt glyph) —
  // wins over the monochrome G.terminal glyph in AppIconTile/Dock so the
  // launcher, dock, and App Hub all show the recognizable black terminal mark.
  terminal: '/icons/terminal.svg',
  chrome: '/icons/chrome.svg',
  browser: '/icons/chrome.svg',
  'browser-stream': '/icons/chrome.svg',
  firefox: '/icons/firefox.svg',
  gimp: '/icons/gimp.svg',
  blender: '/icons/blender.svg',
  inkscape: '/icons/inkscape.svg',
  libreoffice: '/icons/libreoffice.svg',
  // First-party Vulos-ecosystem apps ship their own coloured brand marks under
  // public/product-logos/ (same-origin, redistributable — they're ours). These
  // are the recognizable logos people expect to see in the launcher/App Hub and
  // in screenshots (Ofisi, lilmail, envoir, …). A brand mark WINS over the
  // monochrome glyph in AppIconTile so the app reads as itself, not a generic
  // system tile.
  ofisi: '/product-logos/ofisi.svg',
  lilmail: '/product-logos/lilmail.svg',
  mail: '/product-logos/lilmail.svg',
  envoir: '/product-logos/envoir.svg',
  kerf: '/product-logos/kerf.svg',
  llmux: '/product-logos/llmux.svg',
  kotva: '/product-logos/kotva.svg',
  aql: '/product-logos/aql.svg',
  vuna: '/product-logos/vuna.svg',
  kilio: '/product-logos/kilio.svg',
  soko: '/product-logos/soko.svg',
  gitstate: '/product-logos/gitstate.svg',
  wede: '/product-logos/wede.svg',
  flowstock: '/product-logos/flowstock.svg',
  magnetite: '/product-logos/magnetite.svg',

  // Catalog (self-hosted) apps — same-origin coloured tiles we drew ourselves
  // under public/icons/ (a flat brand-hue squircle + a simple white geometric
  // glyph). These are original marks, NOT copied third-party logos, so they're
  // safe to redistribute and give the App Hub a real, colourful catalog instead
  // of grey letter tiles. Keyed by the registry.json app id.
  android: '/icons/android.svg',
  ardour: '/icons/ardour.svg',
  audacity: '/icons/audacity.svg',
  audiomass: '/icons/audiomass.svg',
  cinny: '/icons/cinny.svg',
  cockpit: '/icons/cockpit.svg',
  'code-server': '/icons/code-server.svg',
  conduit: '/icons/conduit.svg',
  darktable: '/icons/darktable.svg',
  'diagrams-net': '/icons/diagrams-net.svg',
  drawio: '/icons/drawio.svg',
  element: '/icons/element.svg',
  'element-call': '/icons/element-call.svg',
  excalidraw: '/icons/excalidraw.svg',
  filezilla: '/icons/filezilla.svg',
  geany: '/icons/geany.svg',
  gitea: '/icons/gitea.svg',
  gnucash: '/icons/gnucash.svg',
  grafana: '/icons/grafana.svg',
  hoppscotch: '/icons/hoppscotch.svg',
  httpbin: '/icons/httpbin.svg',
  immich: '/icons/immich.svg',
  jellyfin: '/icons/jellyfin.svg',
  'jitsi-meet': '/icons/jitsi-meet.svg',
  jupyter: '/icons/jupyter.svg',
  kdenlive: '/icons/kdenlive.svg',
  keepassxc: '/icons/keepassxc.svg',
  kicad: '/icons/kicad.svg',
  libretranslate: '/icons/libretranslate.svg',
  lmms: '/icons/lmms.svg',
  lutris: '/icons/lutris.svg',
  memos: '/icons/memos.svg',
  minio: '/icons/minio.svg',
  minipaint: '/icons/minipaint.svg',
  navidrome: '/icons/navidrome.svg',
  nginx: '/icons/nginx.svg',
  'obs-studio': '/icons/obs-studio.svg',
  octave: '/icons/octave.svg',
  qbittorrent: '/icons/qbittorrent.svg',
  qgis: '/icons/qgis.svg',
  shotcut: '/icons/shotcut.svg',
  steam: '/icons/steam.svg',
  'svg-edit': '/icons/svg-edit.svg',
  syncthing: '/icons/syncthing.svg',
  transmission: '/icons/transmission.svg',
  'uptime-kuma': '/icons/uptime-kuma.svg',
  vaultwarden: '/icons/vaultwarden.svg',
  vlc: '/icons/vlc.svg',
  wine: '/icons/wine.svg',
}

// Per-app hue. The shell tints each first-party glyph tile with a RESTRAINED
// wash of its app colour (the glyph carries the hue; the tile stays near-neutral)
// so the launcher reads as "considered colour", not a rainbow icon pack. The
// hues below are a deliberately harmonious spread — distinct enough to tell apps
// apart at a glance, muted enough to sit together. App Hub also reads this map.
// eslint-disable-next-line react-refresh/only-export-components
export const APP_COLORS = {
  // Core builtins — one coherent, distinguishable palette.
  terminal: '#14B8A6', activity: '#22D3EE', files: '#F59E0B', drive: '#3B82F6',
  assistant: '#8B5CF6', dashboard: '#6366F1', apphub: '#EC4899', persona: '#94A3B8',
  settings: '#94A3B8', disks: '#EF4444', packages: '#10B981', drivers: '#818CF8',
  authenticator: '#22C55E', vault: '#64748B', messages: '#38BDF8', peering: '#38BDF8',
  relay: '#38BDF8', mail: '#2563EB', lilmail: '#F2674E', compose: '#2563EB',
  calendar: '#F97316', 'vulos-calendar': '#F97316', contacts: '#F43F5E',
  'vulos-contacts': '#F43F5E', 'vulos-phone': '#22C55E', office: '#DC2626', library: '#F97316', gallery: '#06B6D4',
  chat: '#38BDF8',
  browser: '#4285F4', 'browser-stream': '#4285F4',
  firefox: '#FF7139', gimp: '#5C5543', blender: '#EA7600',
  inkscape: '#000', libreoffice: '#18A303', vlc: '#FF8800', audacity: '#0000CC',
  kicad: '#314CB0', keepassxc: '#6CAC4D', filezilla: '#BF0000', transmission: '#B91C1C',
  freecad: '#374DF5', godot: '#478CBF', obs: '#302E31', kdenlive: '#527EB2',
  darktable: '#97A8A0', shotcut: '#115740', wireshark: '#1679A7', remmina: '#00457C',
  qbittorrent: '#2F67BA', geany: '#347C2C',
  adminer: '#43853D', 'sqlite-web': '#003B57', minio: '#C72C48', gitea: '#609926',
  grafana: '#F46800', prometheus: '#E6522C', ttyd: '#4EC9B0', httpbin: '#6C8EBF',
  jupyter: '#F37626', nginx: '#009639', caddy: '#1F88C0', syncthing: '#0891B2',
  miniflux: '#F59E0B', navidrome: '#8B5CF6', headscale: '#6366F1',
  wede: '#6366F1', cockpit: '#0066CC',
  // Catalog apps — brand-appropriate hues matching the coloured tiles under
  // public/icons/ (keyed by registry.json id). Used both for the App Hub tint
  // wash and to keep the tile/glyph a coherent set.
  android: '#3DDC84', ardour: '#B33A3A', audiomass: '#E6842A', cinny: '#B14FD8',
  'code-server': '#007ACC', conduit: '#0F9D8C', 'diagrams-net': '#F08705',
  drawio: '#F08705', element: '#0DBD8B', 'element-call': '#0DBD8B',
  excalidraw: '#6965DB', gnucash: '#5B8A0E', hoppscotch: '#10B981',
  immich: '#4250AF', jellyfin: '#7B4FCE', 'jitsi-meet': '#1E65AF',
  libretranslate: '#1E88A8', lmms: '#0F8CB0', lutris: '#E8663B', memos: '#14B8A6',
  minipaint: '#E74C3C', 'obs-studio': '#302E31', octave: '#1E7AB5', qgis: '#589632',
  steam: '#1B2838', 'svg-edit': '#D97706', 'uptime-kuma': '#3B9E5D',
  vaultwarden: '#175DDC', wine: '#7A1F3D',
}

// eslint-disable-next-line react-refresh/only-export-components
export const APP_LETTERS = {
  'sqlite-web': 'S', ttyd: 'T', httpbin: 'H', keepassxc: 'K', obs: 'O',
  vlc: 'V', gimp: 'G', kicad: 'K', freecad: 'F',
}

// ── Tile chrome — injected once. Kept in CSS (not inline) so the hover/theme
//    states can react instantly and stay theme-aware without a re-render. ──
const styleId = 'vulos-appicon-css'
if (typeof document !== 'undefined' && !document.getElementById(styleId)) {
  const style = document.createElement('style')
  style.id = styleId
  style.textContent = `
    .vula-itile{
      display:flex; align-items:center; justify-content:center; flex-shrink:0;
      background: linear-gradient(160deg, var(--bg-elevated), color-mix(in srgb, var(--bg-elevated) 66%, var(--bg-surface)));
      border:1px solid color-mix(in srgb, #fff 8%, var(--border-strong));
      box-shadow: inset 0 1px 0 rgba(255,255,255,.05), var(--shadow-sm, 0 1px 2px rgba(0,0,0,.4));
      color: var(--text-secondary);
      overflow:hidden;
      transition:
        transform var(--motion-base,.2s) var(--ease-out, cubic-bezier(.16,1,.3,1)),
        color var(--motion-fast,.12s) var(--ease-standard, ease),
        border-color var(--motion-fast,.12s) var(--ease-standard, ease),
        box-shadow var(--motion-fast,.12s) var(--ease-standard, ease);
    }
    [data-theme="light"] .vula-itile{
      box-shadow: inset 0 1px 0 rgba(255,255,255,.85), var(--shadow-sm, 0 1px 2px rgba(15,23,42,.06));
      border-color: var(--border-strong);
    }
    .vula-itile:hover, .vula-itile.is-hover{
      color: var(--text-primary);
      transform: translateY(-3px);
      border-color: color-mix(in srgb, var(--accent) 45%, var(--border-strong));
      box-shadow: inset 0 1px 0 rgba(255,255,255,.06), 0 10px 26px -10px color-mix(in srgb, var(--accent) 45%, transparent);
    }
    [data-theme="light"] .vula-itile:hover, [data-theme="light"] .vula-itile.is-hover{
      box-shadow: inset 0 1px 0 rgba(255,255,255,.9), 0 10px 26px -10px color-mix(in srgb, var(--accent) 38%, transparent);
    }
    .vula-itile:active{ transform: translateY(-1px) scale(.955); transition-duration: .06s; }
    @media (prefers-reduced-motion: reduce){ .vula-itile:hover, .vula-itile.is-hover, .vula-itile:active{ transform:none; } }
    .vula-itile svg{ display:block; }
    .vula-itile-img{ object-fit:contain; }

    /* ── Tinted variant — a restrained wash of the app's hue. The glyph carries
          the colour; the tile stays near-neutral so a grid of them reads as one
          considered set, not a rainbow. Hue arrives via the --tile-accent var. */
    .vula-itile[data-tint]{
      background: linear-gradient(160deg,
        color-mix(in srgb, var(--tile-accent) 13%, var(--bg-elevated)),
        color-mix(in srgb, var(--tile-accent) 4%, var(--bg-surface)));
      border-color: color-mix(in srgb, var(--tile-accent) 24%, var(--border-strong));
      color: color-mix(in srgb, var(--tile-accent) 72%, var(--text-secondary));
    }
    .vula-itile[data-tint]:hover, .vula-itile[data-tint].is-hover{
      color: color-mix(in srgb, var(--tile-accent) 90%, var(--text-primary));
      border-color: color-mix(in srgb, var(--tile-accent) 55%, var(--border-strong));
      box-shadow: inset 0 1px 0 rgba(255,255,255,.06),
                  0 12px 30px -12px color-mix(in srgb, var(--tile-accent) 60%, transparent);
    }
    [data-theme="light"] .vula-itile[data-tint]{
      color: color-mix(in srgb, var(--tile-accent) 68%, var(--text-secondary));
      background: linear-gradient(160deg,
        color-mix(in srgb, var(--tile-accent) 15%, #fff),
        color-mix(in srgb, var(--tile-accent) 5%, var(--bg-surface)));
      border-color: color-mix(in srgb, var(--tile-accent) 26%, var(--border-strong));
    }
    [data-theme="light"] .vula-itile[data-tint]:hover, [data-theme="light"] .vula-itile[data-tint].is-hover{
      box-shadow: inset 0 1px 0 rgba(255,255,255,.9),
                  0 12px 30px -12px color-mix(in srgb, var(--tile-accent) 45%, transparent);
    }

    /* Launcher entrance — a gentle staggered rise. Opt-in via .vula-tile-in with
       a per-item --tile-i index for the delay; honours reduced-motion. */
    @keyframes vula-tile-rise{ from{ opacity:0; transform: translateY(10px) scale(.94); } to{ opacity:1; transform:none; } }
    .vula-tile-in{ animation: vula-tile-rise .42s var(--ease-out, cubic-bezier(.16,1,.3,1)) both;
                   animation-delay: calc(var(--tile-i, 0) * 22ms); }
    @media (prefers-reduced-motion: reduce){ .vula-tile-in{ animation: none; } }
  `
  document.head.appendChild(style)
}

// ── The glyph library ────────────────────────────────────────────────────
// Each entry is the *inside* of a 0 0 24 24 <svg>. The wrapping <svg> owns
// fill:none / stroke:currentColor / width:1.7 / round caps+joins, so glyphs
// stay hairline-consistent at every size. A handful of tiny indicator dots
// opt into fill via fill="currentColor" stroke="none".
const G = {
  terminal: (
    <>
      <rect x="3" y="4" width="18" height="16" rx="3" />
      <path d="M7.4 9.5l2.9 2.5-2.9 2.5" />
      <path d="M12.8 14.6h3.8" />
    </>
  ),
  activity: (
    <>
      <rect x="3" y="4" width="18" height="16" rx="3" />
      <path d="M6 14l2.8-4 2.4 3 2.2-4.2L16 14" />
    </>
  ),
  files: (
    <path d="M3 7.5A2 2 0 015 5.5h3.3l2 2H19a2 2 0 012 2v6.7a2 2 0 01-2 2H5a2 2 0 01-2-2z" />
  ),
  drive: (
    <path d="M7.7 18.4A4 4 0 016.9 10.6 5.3 5.3 0 0117.1 11a3.7 3.7 0 01-.2 7.4z" />
  ),
  assistant: (
    <>
      <path d="M9.9 15.5A2 2 0 008.5 14.1l-6.1-1.6a.5.5 0 010-1L8.5 9.9A2 2 0 009.9 8.5l1.6-6.1a.5.5 0 01.96 0L14.1 8.5a2 2 0 001.4 1.4l6.1 1.6a.5.5 0 010 1L15.5 14.1a2 2 0 00-1.4 1.4l-1.6 6.1a.5.5 0 01-.96 0z" />
      <path d="M19 3.4v2.4M17.8 4.6h2.4" />
    </>
  ),
  settings: (
    <>
      <path d="M3.5 6h3.3M11.2 6H20.5" />
      <circle cx="9" cy="6" r="2" />
      <path d="M3.5 12h8.3M16.2 12H20.5" />
      <circle cx="14" cy="12" r="2" />
      <path d="M3.5 18h2.1M10 18H20.5" />
      <circle cx="7.8" cy="18" r="2" />
    </>
  ),
  apphub: (
    <>
      <rect x="3.5" y="3.5" width="6.5" height="6.5" rx="1.9" />
      <rect x="14" y="3.5" width="6.5" height="6.5" rx="1.9" />
      <rect x="3.5" y="14" width="6.5" height="6.5" rx="1.9" />
      <path d="M17.25 14v6.5M14 17.25h6.5" />
    </>
  ),
  disks: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <circle cx="12" cy="12" r="2.3" />
      <path d="M12 3.5a8.5 8.5 0 018.5 8.5" />
    </>
  ),
  packages: (
    <>
      <path d="M12 3l7.8 4.3v9.4L12 21l-7.8-4.3V7.3z" />
      <path d="M4.4 7.5L12 11.8l7.6-4.3M12 11.8V21" />
    </>
  ),
  drivers: (
    <>
      <rect x="6.5" y="6.5" width="11" height="11" rx="2" />
      <rect x="10" y="10" width="4" height="4" rx="1" />
      <path d="M9 3.6v2.9M12 3.6v2.9M15 3.6v2.9M9 17.5v2.9M12 17.5v2.9M15 17.5v2.9M3.6 9h2.9M3.6 12h2.9M3.6 15h2.9M17.5 9h2.9M17.5 12h2.9M17.5 15h2.9" />
    </>
  ),
  dashboard: (
    <>
      <rect x="3.5" y="3.5" width="8" height="7" rx="1.7" />
      <rect x="3.5" y="13" width="8" height="7.5" rx="1.7" />
      <rect x="14.5" y="3.5" width="6" height="17" rx="1.7" />
    </>
  ),
  authenticator: (
    <>
      <path d="M12 3l7 2.5v5c0 5-3.2 8.4-7 10.5-3.8-2.1-7-5.5-7-10.5v-5z" />
      <path d="M9 12l2.1 2.1L15 9.6" />
    </>
  ),
  vault: (
    <>
      <rect x="5" y="10.4" width="14" height="10.1" rx="2.4" />
      <path d="M8 10.4V8a4 4 0 018 0v2.4" />
      <path d="M12 14.5v2.4" />
    </>
  ),
  library: (
    <>
      <path d="M4.5 6.4A2 2 0 016.5 4.4H19v13.2H6.5a2 2 0 00-2 2z" />
      <path d="M4.5 17.6A2 2 0 016.5 15.6H19" />
      <path d="M8 8.4h7M8 11h5" />
    </>
  ),
  gallery: (
    <>
      <rect x="3.5" y="4.5" width="17" height="15" rx="2.5" />
      <circle cx="8.6" cy="9.6" r="1.6" />
      <path d="M3.9 16.2l4.1-3.9 3 2.8 3-2.8 4.2 4.2" />
    </>
  ),
  social: (
    <>
      <circle cx="6" cy="12" r="2.4" />
      <circle cx="17" cy="6.6" r="2.4" />
      <circle cx="17" cy="17.4" r="2.4" />
      <path d="M8.1 10.9l6.8-3.3M8.1 13.1l6.8 3.3" />
    </>
  ),
  mail: (
    <>
      <rect x="3" y="5" width="18" height="14" rx="2.6" />
      <path d="M3.7 6.6L12 12.4l8.3-5.8" />
    </>
  ),
  chat: (
    <>
      <path d="M4 6.4A2.5 2.5 0 016.5 3.9h11A2.5 2.5 0 0120 6.4v6.2A2.5 2.5 0 0117.5 15H10l-4 3.4V15H6.5A2.5 2.5 0 014 12.6z" />
      <circle cx="8.7" cy="9.5" r=".85" fill="currentColor" stroke="none" />
      <circle cx="12" cy="9.5" r=".85" fill="currentColor" stroke="none" />
      <circle cx="15.3" cy="9.5" r=".85" fill="currentColor" stroke="none" />
    </>
  ),
  office: (
    <>
      <path d="M6.5 3.5h7.2L18.5 8.3v11.2a1 1 0 01-1 1h-11a1 1 0 01-1-1v-15a1 1 0 011-1z" />
      <path d="M13.6 3.5V8.3h4.9" />
      <path d="M8.6 12.4h6.8M8.6 15.4h6.8M8.6 18.2h4.4" />
    </>
  ),
  calendar: (
    <>
      <rect x="3.5" y="5" width="17" height="15.5" rx="2.5" />
      <path d="M3.5 9.6h17M8 3v4M16 3v4" />
    </>
  ),
  contacts: (
    <>
      <circle cx="12" cy="9" r="3.4" />
      <path d="M5.6 19.6a6.5 6.5 0 0112.8 0" />
    </>
  ),
  calculator: (
    <>
      <rect x="5" y="3.4" width="14" height="17.2" rx="2.4" />
      <path d="M8 8h8" />
      <circle cx="8.6" cy="12.4" r=".85" fill="currentColor" stroke="none" />
      <circle cx="12" cy="12.4" r=".85" fill="currentColor" stroke="none" />
      <circle cx="15.4" cy="12.4" r=".85" fill="currentColor" stroke="none" />
      <circle cx="8.6" cy="16" r=".85" fill="currentColor" stroke="none" />
      <circle cx="12" cy="16" r=".85" fill="currentColor" stroke="none" />
      <circle cx="15.4" cy="16" r=".85" fill="currentColor" stroke="none" />
    </>
  ),
  clock: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 7.4V12l3.1 2" />
    </>
  ),
  document: (
    <>
      <path d="M6.5 3.5h7.2L18.5 8.3v11.2a1 1 0 01-1 1h-11a1 1 0 01-1-1v-15a1 1 0 011-1z" />
      <path d="M13.6 3.5V8.3h4.9" />
      <path d="M8.6 13h6.8M8.6 16h4.4" />
    </>
  ),
  editor: (
    <>
      <rect x="5.5" y="3.5" width="13" height="17" rx="2" />
      <path d="M9 8.5h6M9 12h6M9 15.5h4" />
    </>
  ),
  brush: (
    <>
      <path d="M14 6.4l3.6 3.6" />
      <path d="M4.6 19.4l2.9-.6a3 3 0 001.5-.8l9-9a2 2 0 000-2.9l-.6-.6a2 2 0 00-2.9 0l-9 9a3 3 0 00-.8 1.5z" />
    </>
  ),
  weather: (
    <>
      <path d="M8.6 18.4A3.4 3.4 0 018.1 11.7a4.6 4.6 0 018.7.6 3.2 3.2 0 01-.2 6.1z" />
      <path d="M15.1 8.2a3.4 3.4 0 00-3.5-3.2" />
      <path d="M18.3 6.4l1-.6M17.9 9.8l1 .3" />
    </>
  ),
  camera: (
    <>
      <path d="M4 8.6A2 2 0 016 6.6h1.6l1-2h4.8l1 2H18a2 2 0 012 2v8.5a2 2 0 01-2 2H6a2 2 0 01-2-2z" />
      <circle cx="12" cy="13.1" r="3.3" />
    </>
  ),
  maps: (
    <>
      <path d="M12 21s6.4-6 6.4-11a6.4 6.4 0 10-12.8 0c0 5 6.4 11 6.4 11z" />
      <circle cx="12" cy="10" r="2.4" />
    </>
  ),
  music: (
    <>
      <path d="M9 18V6.4l10-2v11.6" />
      <circle cx="6.5" cy="18" r="2.5" />
      <circle cx="16.5" cy="16" r="2.5" />
    </>
  ),
  phone: (
    <path d="M6.4 3.6h3l1.4 3.9-2 1.5a11 11 0 005 5l1.5-2 3.9 1.4v3a2 2 0 01-2.2 2A15.6 15.6 0 014.4 5.8 2 2 0 016.4 3.6z" />
  ),
  screenshot: (
    <>
      <path d="M8 4H6a2 2 0 00-2 2v2M16 4h2a2 2 0 012 2v2M8 20H6a2 2 0 01-2-2v-2M16 20h2a2 2 0 002-2v-2" />
      <rect x="9" y="9" width="6" height="6" rx="1.2" />
    </>
  ),
  system: (
    <>
      <rect x="3.5" y="4.5" width="17" height="12" rx="2" />
      <path d="M8.5 20.5h7M12 16.5v4" />
    </>
  ),
  video: (
    <>
      <rect x="3.5" y="5.5" width="17" height="13" rx="2.5" />
      <path d="M10 9.4l4.6 2.6-4.6 2.6z" />
    </>
  ),
  microphone: (
    <>
      <rect x="9" y="3.5" width="6" height="10" rx="3" />
      <path d="M6 11a6 6 0 0012 0M12 17.2v3.3M9 20.5h6" />
    </>
  ),
  globe: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M3.5 12h17M12 3.5c2.4 2.3 2.4 14.7 0 17M12 3.5c-2.4 2.3-2.4 14.7 0 17" />
    </>
  ),
  relay: (
    <>
      <circle cx="12" cy="12" r="2.3" />
      <path d="M7 7a7 7 0 000 10M17 7a7 7 0 010 10M9.2 9.2a4 4 0 000 5.6M14.8 9.2a4 4 0 010 5.6" />
    </>
  ),
}

// ── Settings iconography ──────────────────────────────────────────────────
// The Settings nav used to mix true emoji (🔔 📶 🔉 ⚡ 🔍 🌐 🔗 👤 🛡) with
// loose Unicode geometric glyphs — a rainbow of mismatched sources that broke
// the "one coherent set" rule. These are drawn on the SAME 24×24 grid with the
// SAME 1.7 stroke as the app glyphs above, inherit currentColor, and are keyed
// by the settings section id so the rail reads as one designed system.
const SG = {
  ai: G.assistant,
  models: (
    <>
      <path d="M12 3l7.8 4.3v9.4L12 21l-7.8-4.3V7.3z" />
      <path d="M4.4 7.5L12 11.8l7.6-4.3M12 11.8V21" />
    </>
  ),
  aiapps: G.apphub,
  appearance: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 3.5a8.5 8.5 0 010 17z" fill="currentColor" stroke="none" />
    </>
  ),
  notifications: (
    <>
      <path d="M6.6 10.2a5.4 5.4 0 0110.8 0c0 3.8 1.1 5.2 1.9 6a.6.6 0 01-.4 1H5.1a.6.6 0 01-.4-1c.8-.8 1.9-2.2 1.9-6z" />
      <path d="M10 20a2 2 0 004 0" />
    </>
  ),
  wifi: (
    <>
      <path d="M4.8 9.4a11 11 0 0114.4 0M7.6 12.5a7 7 0 018.8 0M10.3 15.6a2.6 2.6 0 013.4 0" />
      <circle cx="12" cy="18.7" r=".95" fill="currentColor" stroke="none" />
    </>
  ),
  bluetooth: <path d="M8 7.4l8 9.2-4 3.4V3.9l4 3.5-8 9.2" />,
  audio: (
    <>
      <path d="M4 9.5h3.4L12 5.6v12.8L7.4 14.5H4z" />
      <path d="M15.4 9.2a4 4 0 010 5.6M18 6.8a7.4 7.4 0 010 10.4" />
    </>
  ),
  display: G.system,
  energy: (
    <>
      <rect x="3" y="7" width="15" height="10" rx="2.4" />
      <path d="M20.4 10.4v3.2" />
      <path d="M10.6 9.4L8 12.7h2.6l-.9 2.9L12.6 12H10z" fill="currentColor" stroke="none" />
    </>
  ),
  location: G.maps,
  vault: (
    <>
      <path d="M4.6 11.4a7.4 7.4 0 0112.5-4.9L19.6 9" />
      <path d="M19.4 12.6a7.4 7.4 0 01-12.5 4.9L4.4 15" />
      <path d="M19.6 4.6V9h-4.4M4.4 19.4V15h4.4" />
    </>
  ),
  recall: (
    <>
      <circle cx="10.6" cy="10.6" r="6" />
      <path d="M15 15l4.6 4.6" />
    </>
  ),
  storage: (
    <>
      <ellipse cx="12" cy="6.2" rx="6.8" ry="2.7" />
      <path d="M5.2 6.2v11.6c0 1.5 3 2.7 6.8 2.7s6.8-1.2 6.8-2.7V6.2" />
      <path d="M5.2 12c0 1.5 3 2.7 6.8 2.7s6.8-1.2 6.8-2.7" />
    </>
  ),
  storagemode: (
    <>
      <rect x="3" y="6.5" width="18" height="11" rx="2.4" />
      <circle cx="16.6" cy="12" r="1.1" fill="currentColor" stroke="none" />
      <path d="M6.4 12h6.4" />
    </>
  ),
  connmode: G.social,
  network: G.globe,
  domain: (
    <>
      <path d="M9.6 14.4l4.8-4.8" />
      <path d="M8.2 12.2l-2 2a3.5 3.5 0 004.9 4.9l2-2M15.8 11.8l2-2a3.5 3.5 0 00-4.9-4.9l-2 2" />
    </>
  ),
  relay: G.relay,
  cdn: G.drive,
  turnSettings: <path d="M7 8h11l-3.2-3.2M17 16H6l3.2 3.2" />,
  webhooks: (
    <>
      <circle cx="6.8" cy="6.4" r="2.2" />
      <circle cx="6.8" cy="17.6" r="2.2" />
      <circle cx="17.2" cy="12" r="2.2" />
      <path d="M6.8 8.6v6.8M8.9 7c4 .4 5.8 2.2 6.1 5.3M15 13.6c-1 2.6-3.1 3.9-6.1 4" />
    </>
  ),
  developer: <path d="M8.4 8L4.4 12l4 4M15.6 8l4 4-4 4M13.6 5.4l-3.2 13.2" />,
  users: (
    <>
      <circle cx="9" cy="9" r="3" />
      <path d="M3.8 19a5.2 5.2 0 0110.4 0" />
      <path d="M15.6 6.5a3 3 0 010 5M16.6 14.5a5.2 5.2 0 013.6 4.5" />
    </>
  ),
  pin: (
    <>
      <rect x="4" y="4" width="16" height="16" rx="3.2" />
      <circle cx="8.6" cy="8.6" r=".95" fill="currentColor" stroke="none" />
      <circle cx="12" cy="8.6" r=".95" fill="currentColor" stroke="none" />
      <circle cx="15.4" cy="8.6" r=".95" fill="currentColor" stroke="none" />
      <circle cx="8.6" cy="12" r=".95" fill="currentColor" stroke="none" />
      <circle cx="12" cy="12" r=".95" fill="currentColor" stroke="none" />
      <circle cx="15.4" cy="12" r=".95" fill="currentColor" stroke="none" />
      <circle cx="8.6" cy="15.4" r=".95" fill="currentColor" stroke="none" />
      <circle cx="12" cy="15.4" r=".95" fill="currentColor" stroke="none" />
      <circle cx="15.4" cy="15.4" r=".95" fill="currentColor" stroke="none" />
    </>
  ),
  fingerprint: (
    <>
      <path d="M8 7.2a7 7 0 018 0" />
      <path d="M5.8 10.4a9.2 9.2 0 0112.4 0" />
      <path d="M9 12.2a4 4 0 016 0v1.4" />
      <path d="M9 15.4a4 4 0 006 0" />
      <path d="M12 12.2v6.4M9 18.4v-2.6" />
    </>
  ),
  account: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <circle cx="12" cy="10" r="2.7" />
      <path d="M6.7 18.6a5.5 5.5 0 0110.6 0" />
    </>
  ),
  offlinedata: (
    <>
      <path d="M7.7 16.6A3.7 3.7 0 017 9.4 5.2 5.2 0 0116.2 9.9" />
      <path d="M18 12.4a3.7 3.7 0 01-1.2 4.2" />
      <path d="M4 4.2l16 16" />
    </>
  ),
  dataexport: (
    <>
      <path d="M12 4v10M8.2 10.4L12 14.2l3.8-3.8" />
      <path d="M5 18.4h14" />
    </>
  ),
  security: G.authenticator,
  osupdate: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 16.2V8M8.4 11.6L12 8l3.6 3.6" />
    </>
  ),
  boxhealth: <path d="M3.4 12h3.9l1.9-5 3.2 10 2.4-5h5.8" />,
  about: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 11v5" />
      <circle cx="12" cy="7.9" r=".95" fill="currentColor" stroke="none" />
    </>
  ),
  // Theme-picker chips (Appearance panel).
  sun: (
    <>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2.6v2.6M12 18.8v2.6M4.3 4.3l1.9 1.9M17.8 17.8l1.9 1.9M2.6 12h2.6M18.8 12h2.6M4.3 19.7l1.9-1.9M17.8 6.2l1.9-1.9" />
    </>
  ),
  moon: <path d="M20 13.6A8 8 0 019.4 4.2 7 7 0 1020 13.6z" />,
  clock: G.clock,
}

// SettingsIcon — a settings-section glyph on the shared 24×24 / 1.7-stroke
// chrome. `name` is the section id (or a theme-chip key). Unknown names render
// nothing so a new section never crashes the rail.
export function SettingsIcon({ name, size = 16, style }) {
  const node = SG[name]
  if (!node) return null
  return <GlyphSvg node={node} size={size} style={{ display: 'block', ...style }} />
}

// Public glyph map, keyed by app id (aliases share a drawing). Names here that
// already existed (terminal/activity/files/persona/apphub/library/gallery/
// disks/packages/drivers/chat) are preserved; the rest broaden coverage of the
// real builtin ids so the launcher/dock read as one set.
const icons = {
  // Original ids — kept stable.
  terminal: G.terminal,
  activity: G.activity,
  files: G.files,
  persona: G.settings,
  apphub: G.apphub,
  library: G.library,
  gallery: G.gallery,
  disks: G.disks,
  packages: G.packages,
  drivers: G.drivers,
  chat: G.chat,
  // Broadened builtin coverage.
  drive: G.drive,
  assistant: G.assistant,
  settings: G.settings,
  dashboard: G.dashboard,
  authenticator: G.authenticator,
  vault: G.vault,
  lilmail: G.mail,
  mail: G.mail,
  compose: G.mail,
  messages: G.chat,
  peering: G.relay,
  relay: G.relay,
  office: G.office,
  'vulos-calendar': G.calendar,
  calendar: G.calendar,
  'vulos-contacts': G.contacts,
  contacts: G.contacts,
  calculator: G.calculator,
  clock: G.clock,
  'text-editor': G.editor,
  'image-editor': G.brush,
  weather: G.weather,
  camera: G.camera,
  music: G.music,
  phone: G.phone,
  'vulos-phone': G.phone,
  screenshot: G.screenshot,
  'system-info': G.system,
  video: G.video,
  'voice-recorder': G.microphone,
  browser: G.globe,
  'browser-stream': G.globe,
}

// Shared <svg> chrome so every glyph is hairline-consistent at any size.
function GlyphSvg({ node, size, className, style }) {
  return (
    <svg
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill="none"
      stroke="currentColor"
      strokeWidth={1.7}
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      style={style}
    >
      {node}
    </svg>
  )
}

// Inline glyph — used in titlebars, mission control, the dock, mobile chrome.
// Inherits `currentColor`; `color` can force a specific token/hue.
export default function AppIcon({ id, size = 16, color, style }) {
  const node = icons[id]
  if (!node) {
    return (
      <span style={{ fontSize: size * 0.72, lineHeight: 1, fontWeight: 600, ...style }}>
        {id?.[0]?.toUpperCase() || '?'}
      </span>
    )
  }
  return (
    <GlyphSvg
      node={node}
      size={size}
      style={{ color: color || 'currentColor', display: 'block', ...style }}
    />
  )
}

// Tile — used by the dock and launchpad. A neutral squircle with an inset
// top-light and hairline border; the glyph inherits --text-secondary and rises
// to --text-primary on hover while the tile picks up the accent glow. Real
// third-party brand logos (Chrome/Firefox/…) render as their own artwork on the
// same neutral tile so the surface still reads as one set. Falls back to a
// locally-installed system icon, then a monochrome letter tile.
export function AppIconTile({ id, size = 48, unicode }) {
  const node = icons[id]
  const brandLogo = APP_LOGOS[id]
  const tint = APP_COLORS[id]
  const [logoFailed, setLogoFailed] = useState(false)
  const [hover, setHover] = useState(false)
  const radius = Math.round(size * 0.28)
  const glyphSize = Math.round(size * 0.5)

  const tileProps = {
    className: 'vula-itile' + (hover ? ' is-hover' : ''),
    onMouseEnter: () => setHover(true),
    onMouseLeave: () => setHover(false),
    style: { width: size, height: size, borderRadius: radius },
  }

  // 1. A first-party (or bundled) brand mark WINS — the app should read as
  //    itself. Ofisi/lilmail/envoir/Chrome/… render their own coloured artwork
  //    on the neutral tile so the surface still reads as one coherent set.
  if (brandLogo && !logoFailed) {
    return (
      <div {...tileProps}>
        <img
          src={brandLogo}
          alt=""
          className="vula-itile-img"
          style={{ width: '70%', height: '70%' }}
          onError={() => setLogoFailed(true)}
          loading="lazy"
        />
      </div>
    )
  }

  // 2. First-party glyph — tinted with the app's restrained hue via --tile-accent.
  if (node) {
    const tinted = tint
      ? { ...tileProps, 'data-tint': '', style: { ...tileProps.style, '--tile-accent': tint } }
      : tileProps
    return (
      <div {...tinted}>
        <GlyphSvg node={node} size={glyphSize} />
      </div>
    )
  }

  // 3. Locally-installed desktop system icon (Debian icon theme) for apps
  //    without a first-party glyph or brand mark.
  if (!logoFailed) {
    return (
      <div {...tileProps}>
        <img
          src={`/api/desktop/icon/${id}`}
          alt=""
          className="vula-itile-img"
          style={{ width: '66%', height: '66%' }}
          onError={() => setLogoFailed(true)}
          loading="lazy"
        />
      </div>
    )
  }

  // 4. Letter fallback — monochrome, token-driven (no rainbow gradient).
  const letter = APP_LETTERS[id] || (unicode || id || '?')[0].toUpperCase()
  return (
    <div {...tileProps} style={{ ...tileProps.style, fontWeight: 600, fontSize: Math.round(size * 0.4), letterSpacing: '-0.01em' }}>
      {letter}
    </div>
  )
}
