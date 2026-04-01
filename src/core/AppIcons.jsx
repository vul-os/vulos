import { useState } from 'react'

// SVG app icons for builtin and registry apps.
// Each icon is a 16x16 viewBox SVG rendered inline.

// Logo URLs for installed apps (shared with AppHub)
export const APP_LOGOS = {
  chrome: '/icons/chrome.svg',
  browser: '/icons/chrome.svg',
  firefox: '/icons/firefox.svg',
  thunderbird: '/icons/thunderbird.svg',
  gimp: '/icons/gimp.svg',
  blender: '/icons/blender.svg',
  inkscape: '/icons/inkscape.svg',
  libreoffice: '/icons/libreoffice.svg',
  vlc: 'https://upload.wikimedia.org/wikipedia/commons/e/e6/VLC_Icon.svg',
  audacity: 'https://upload.wikimedia.org/wikipedia/commons/f/f6/Audacity_Logo.svg',
  kicad: 'https://upload.wikimedia.org/wikipedia/commons/5/59/KiCad-Logo.svg',
  keepassxc: 'https://upload.wikimedia.org/wikipedia/commons/c/1/KeePassXC_icon.svg',
  filezilla: 'https://upload.wikimedia.org/wikipedia/commons/0/01/FileZilla_logo.svg',
  transmission: 'https://upload.wikimedia.org/wikipedia/commons/6/6d/Transmission_icon.png',
  nginx: 'https://upload.wikimedia.org/wikipedia/commons/c/c5/Nginx_logo.svg',
  grafana: 'https://upload.wikimedia.org/wikipedia/commons/a/a1/Grafana_logo.svg',
  jupyter: 'https://upload.wikimedia.org/wikipedia/commons/3/38/Jupyter_logo.svg',
  gitea: 'https://upload.wikimedia.org/wikipedia/commons/b/bb/Gitea_Logo.svg',
  syncthing: 'https://upload.wikimedia.org/wikipedia/commons/8/83/SyncthingAugworGraphic.png',
  wede: 'https://raw.githubusercontent.com/webcrft/wede/main/public/icon.svg',
}

export const APP_COLORS = {
  // Builtins
  terminal: '#4EC9B0', activity: '#3B82F6', files: '#F59E0B', persona: '#8B5CF6',
  browser: '#4285F4', apphub: '#EC4899', library: '#F97316', gallery: '#06B6D4',
  disks: '#EF4444', packages: '#10B981', drivers: '#6366F1', chat: '#3B82F6',
  // Desktop apps
  firefox: '#FF7139', thunderbird: '#0A84FF', gimp: '#5C5543', blender: '#EA7600',
  inkscape: '#000', libreoffice: '#18A303', vlc: '#FF8800', audacity: '#0000CC',
  kicad: '#314CB0', keepassxc: '#6CAC4D', filezilla: '#BF0000', transmission: '#B91C1C',
  freecad: '#374DF5', godot: '#478CBF', obs: '#302E31', kdenlive: '#527EB2',
  darktable: '#97A8A0', shotcut: '#115740', wireshark: '#1679A7', remmina: '#00457C',
  qbittorrent: '#2F67BA', geany: '#347C2C',
  // Web apps
  adminer: '#43853D', 'sqlite-web': '#003B57', minio: '#C72C48', gitea: '#609926',
  grafana: '#F46800', prometheus: '#E6522C', ttyd: '#4EC9B0', httpbin: '#6C8EBF',
  jupyter: '#F37626', nginx: '#009639', caddy: '#1F88C0', syncthing: '#0891B2',
  miniflux: '#F59E0B', navidrome: '#8B5CF6', headscale: '#6366F1',
  wede: '#6366F1', cockpit: '#0066CC',
}

export const APP_LETTERS = {
  'sqlite-web': 'S', ttyd: 'T', httpbin: 'H', keepassxc: 'K', obs: 'O',
  vlc: 'V', gimp: 'G', kicad: 'K', freecad: 'F',
}

// CSS animations for icon hover effects — injected once
const styleId = 'vula-icon-anims'
if (typeof document !== 'undefined' && !document.getElementById(styleId)) {
  const style = document.createElement('style')
  style.id = styleId
  style.textContent = `
    @keyframes termType { 0%,100% { opacity: 0.3; width: 0 } 50% { opacity: 1; width: 3px } }
    @keyframes cogSpin { from { transform-origin: 8px 8px; transform: rotate(0deg) } to { transform-origin: 8px 8px; transform: rotate(360deg) } }
    @keyframes pulse { 0%,100% { opacity: 0.6 } 50% { opacity: 1 } }
    @keyframes wave { 0%,100% { d: path("M3,10 5,6 7,9 9,4 11,8 13,5") } 50% { d: path("M3,8 5,4 7,10 9,6 11,5 13,8") } }
    @keyframes folderPeek { 0%,100% { transform: translateY(0) } 50% { transform: translateY(-1px) } }
    @keyframes gridPulse { 0%,100% { opacity: 0.3 } 50% { opacity: 0.8 } }
    @keyframes chatBounce { 0%,100% { transform: translateY(0) } 30% { transform: translateY(-1px) } 60% { transform: translateY(0.5px) } }
    @keyframes diskSpin { from { transform-origin: 8px 8px; transform: rotate(0deg) } to { transform-origin: 8px 8px; transform: rotate(360deg) } }
    .icon-anim-term .term-cursor { animation: termType 0.8s ease-in-out infinite; }
    .icon-anim-cog > g { animation: cogSpin 3s linear infinite; }
    .icon-anim-pulse polyline { animation: pulse 1.5s ease-in-out infinite; }
    .icon-anim-folder > g { animation: folderPeek 1s ease-in-out infinite; }
    .icon-anim-grid rect:nth-child(4) { animation: gridPulse 1s ease-in-out infinite; }
    .icon-anim-chat > g { animation: chatBounce 0.6s ease-in-out infinite; }
    .icon-anim-disk path { animation: diskSpin 4s linear infinite; }
  `
  document.head.appendChild(style)
}

// Colorful builtin icons
const icons = {
  terminal: (
    <g>
      <rect x="1" y="2" width="14" height="12" rx="2.5" fill="#1a2332" stroke="#4EC9B0" strokeWidth="0.8" />
      <path d="M4 6l3 2-3 2" stroke="#4EC9B0" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" fill="none" />
      <line x1="9" y1="10" x2="12" y2="10" stroke="#4EC9B0" strokeWidth="1.2" strokeLinecap="round" opacity="0.7" />
      <rect className="term-cursor" x="9" y="9.2" width="0" height="1.6" rx="0.3" fill="#4EC9B0" />
    </g>
  ),
  activity: (
    <g>
      <rect x="1" y="2" width="14" height="12" rx="2.5" fill="#0f1729" stroke="#3B82F6" strokeWidth="0.8" />
      <polyline points="3,10 5,6 7,9 9,4 11,8 13,5" fill="none" stroke="#3B82F6" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" />
      <polyline points="3,10 5,6 7,9 9,4 11,8 13,5 13,12 3,12" fill="#3B82F620" stroke="none" />
    </g>
  ),
  files: (
    <g>
      <path d="M2 5A1.5 1.5 0 013.5 3.5H6l1.5 1.5H12.5A1.5 1.5 0 0114 6.5v5.5a1.5 1.5 0 01-1.5 1.5h-9A1.5 1.5 0 012 12V5z" fill="#F59E0B" opacity="0.9" />
      <path d="M2 6.5h12v6a1.5 1.5 0 01-1.5 1.5h-9A1.5 1.5 0 012 12.5V6.5z" fill="#FBBF24" opacity="0.6" />
    </g>
  ),
  persona: (
    <g>
      <circle cx="8" cy="8" r="7" fill="#8B5CF6" opacity="0.1" />
      <g>
        <path d="M8 2.5l.7 1.6c.5.1 1 .3 1.4.6l1.6-.4.9 1.5-1 1.2c.1.5.1 1 0 1.4l1 1.2-.9 1.5-1.6-.4c-.4.3-.9.5-1.4.6L8 13.5l-.7-1.8c-.5-.1-1-.3-1.4-.6l-1.6.4-.9-1.5 1-1.2c-.1-.5-.1-1 0-1.4l-1-1.2.9-1.5 1.6.4c.4-.3.9-.5 1.4-.6L8 2.5z" fill="#8B5CF6" opacity="0.7" stroke="#A78BFA" strokeWidth="0.5" />
        <circle cx="8" cy="8" r="2" fill="#1a1025" stroke="#A78BFA" strokeWidth="0.7" />
      </g>
    </g>
  ),
  apphub: (
    <g>
      <rect x="2" y="2" width="5.2" height="5.2" rx="1.3" fill="#EC4899" opacity="0.8" />
      <rect x="8.8" y="2" width="5.2" height="5.2" rx="1.3" fill="#F472B6" opacity="0.6" />
      <rect x="2" y="8.8" width="5.2" height="5.2" rx="1.3" fill="#F472B6" opacity="0.6" />
      <rect x="8.8" y="8.8" width="5.2" height="5.2" rx="1.3" fill="#EC4899" opacity="0.3" />
      <line x1="11.4" y1="9.5" x2="11.4" y2="13.3" stroke="white" strokeWidth="1.2" strokeLinecap="round" />
      <line x1="9.5" y1="11.4" x2="13.3" y2="11.4" stroke="white" strokeWidth="1.2" strokeLinecap="round" />
    </g>
  ),
  library: (
    <g>
      <rect x="3" y="2" width="10" height="12" rx="1.5" fill="#F97316" opacity="0.15" />
      <rect x="3" y="2" width="10" height="12" rx="1.5" fill="none" stroke="#F97316" strokeWidth="0.8" />
      <line x1="5" y1="5" x2="11" y2="5" stroke="#FB923C" strokeWidth="1" strokeLinecap="round" />
      <line x1="5" y1="7.5" x2="11" y2="7.5" stroke="#FB923C" strokeWidth="1" strokeLinecap="round" opacity="0.7" />
      <line x1="5" y1="10" x2="9" y2="10" stroke="#FB923C" strokeWidth="1" strokeLinecap="round" opacity="0.5" />
    </g>
  ),
  gallery: (
    <g>
      <rect x="1.5" y="2.5" width="13" height="11" rx="2" fill="#06B6D4" opacity="0.12" />
      <rect x="1.5" y="2.5" width="13" height="11" rx="2" fill="none" stroke="#06B6D4" strokeWidth="0.8" />
      <circle cx="5.5" cy="6" r="1.5" fill="#22D3EE" opacity="0.8" />
      <path d="M1.5 11l3.5-4 2.5 3 2-1.5L14.5 13" stroke="#06B6D4" strokeWidth="1" strokeLinejoin="round" fill="#06B6D420" />
    </g>
  ),
  disks: (
    <g>
      <circle cx="8" cy="8" r="6.5" fill="#EF4444" opacity="0.1" />
      <circle cx="8" cy="8" r="6.5" fill="none" stroke="#EF4444" strokeWidth="0.8" />
      <path d="M8 1.5 A6.5 6.5 0 1 1 1.5 8 L8 8 Z" fill="#EF4444" opacity="0.5" />
      <circle cx="8" cy="8" r="2" fill="#0a0a0a" stroke="#EF4444" strokeWidth="0.6" />
    </g>
  ),
  packages: (
    <g>
      <rect x="2.5" y="1.5" width="11" height="13" rx="2" fill="#10B981" opacity="0.12" />
      <rect x="2.5" y="1.5" width="11" height="13" rx="2" fill="none" stroke="#10B981" strokeWidth="0.8" />
      <path d="M6 1.5V4h4V1.5" stroke="#34D399" strokeWidth="0.8" fill="#10B981" opacity="0.3" />
      <line x1="5.5" y1="7" x2="10.5" y2="7" stroke="#34D399" strokeWidth="0.8" strokeLinecap="round" />
      <line x1="5.5" y1="9.5" x2="10.5" y2="9.5" stroke="#34D399" strokeWidth="0.8" strokeLinecap="round" opacity="0.6" />
      <line x1="5.5" y1="12" x2="8.5" y2="12" stroke="#34D399" strokeWidth="0.8" strokeLinecap="round" opacity="0.4" />
    </g>
  ),
  drivers: (
    <g>
      <rect x="2" y="3" width="12" height="10" rx="2" fill="#6366F1" opacity="0.12" />
      <rect x="2" y="3" width="12" height="10" rx="2" fill="none" stroke="#6366F1" strokeWidth="0.8" />
      <circle cx="5.5" cy="8" r="1.3" fill="#818CF8" opacity="0.8" />
      <circle cx="8" cy="8" r="1.3" fill="#A5B4FC" opacity="0.6" />
      <circle cx="10.5" cy="8" r="1.3" fill="#818CF8" opacity="0.8" />
      <line x1="4" y1="5.5" x2="12" y2="5.5" stroke="#6366F1" strokeWidth="0.6" opacity="0.5" />
    </g>
  ),
  chat: (
    <g>
      <path d="M2 3.5a2 2 0 012-2h8a2 2 0 012 2v5.5a2 2 0 01-2 2H6.5l-3 2.5V11H4a2 2 0 01-2-2V3.5z" fill="#3B82F6" opacity="0.85" />
      <circle cx="5.5" cy="6.5" r="0.8" fill="white" opacity="0.8" />
      <circle cx="8" cy="6.5" r="0.8" fill="white" opacity="0.8" />
      <circle cx="10.5" cy="6.5" r="0.8" fill="white" opacity="0.8" />
    </g>
  ),
}

export default function AppIcon({ id, size = 16, color, style }) {
  const icon = icons[id]
  if (!icon) return <span style={{ fontSize: size * 0.8, lineHeight: 1, ...style }}>{id?.[0]?.toUpperCase() || '?'}</span>

  return (
    <svg
      viewBox="0 0 16 16"
      width={size}
      height={size}
      fill="none"
      style={{ color: color || 'currentColor', display: 'block', ...style }}
    >
      {icon}
    </svg>
  )
}

// For dock/launchpad tiles — renders in a rounded square container with hover animation
// Shows: logo image > builtin SVG icon > letter fallback
export function AppIconTile({ id, size = 48, unicode }) {
  const icon = icons[id]
  const logo = APP_LOGOS[id]
  const color = APP_COLORS[id]
  const [logoFailed, setLogoFailed] = useState(false)
  const [hover, setHover] = useState(false)
  const radius = size * 0.24

  const wrapStyle = {
    width: size, height: size, borderRadius: radius,
    display: 'flex', alignItems: 'center', justifyContent: 'center',
    transition: 'transform 0.2s ease, box-shadow 0.2s ease',
    transform: hover ? 'scale(1.1) translateY(-2px)' : 'scale(1)',
    boxShadow: hover && color ? `0 4px 20px ${color}30` : 'none',
  }

  // Logo image (Chrome, Firefox, etc.)
  const imgSrc = (logo && !logoFailed) ? logo : (!logoFailed ? `/api/desktop/icon/${id}` : null)
  if (imgSrc && !icon) {
    return (
      <div
        onMouseEnter={() => setHover(true)}
        onMouseLeave={() => setHover(false)}
        style={{
          ...wrapStyle,
          background: color ? `${color}12` : '#1a1a1a',
          border: `1px solid ${color ? color + '25' : '#2a2a2a'}`,
          overflow: 'hidden',
        }}
      >
        <img
          src={imgSrc}
          alt=""
          style={{ width: '70%', height: '70%', objectFit: 'contain' }}
          onError={() => setLogoFailed(true)}
          loading="lazy"
        />
      </div>
    )
  }

  // Browser specifically — use the Chrome SVG logo
  if (id === 'browser' && logo && !logoFailed) {
    return (
      <div
        onMouseEnter={() => setHover(true)}
        onMouseLeave={() => setHover(false)}
        style={{
          ...wrapStyle,
          background: '#111827',
          border: '1px solid #1e3a5f',
          overflow: 'hidden',
        }}
      >
        <img
          src={logo}
          alt=""
          style={{ width: '72%', height: '72%', objectFit: 'contain' }}
          onError={() => setLogoFailed(true)}
          loading="lazy"
        />
      </div>
    )
  }

  // Builtin SVG icon with hover animation
  const animClass = {
    terminal: 'icon-anim-term',
    persona: 'icon-anim-cog',
    activity: 'icon-anim-pulse',
    files: 'icon-anim-folder',
    apphub: 'icon-anim-grid',
    chat: 'icon-anim-chat',
    disks: 'icon-anim-disk',
  }
  if (icon) {
    return (
      <div
        onMouseEnter={() => setHover(true)}
        onMouseLeave={() => setHover(false)}
        style={{
          ...wrapStyle,
          background: color ? `${color}10` : '#1a1a1a',
          border: `1px solid ${color ? color + '25' : '#2a2a2a'}`,
        }}
      >
        <svg viewBox="0 0 16 16" width={size * 0.55} height={size * 0.55} fill="none"
          className={hover && animClass[id] ? animClass[id] : ''}>
          {icon}
        </svg>
      </div>
    )
  }

  // Letter fallback
  const letter = APP_LETTERS[id] || (unicode || id || '?')[0].toUpperCase()
  return (
    <div
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      style={{
        ...wrapStyle,
        background: color ? `linear-gradient(135deg, ${color}30, ${color}15)` : '#262626',
        border: `1px solid ${color ? color + '30' : '#333'}`,
        color: color || '#a3a3a3', fontWeight: 700, fontSize: size * 0.38,
      }}
    >
      {letter}
    </div>
  )
}
