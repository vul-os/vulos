import { useState } from 'react'

// SVG app icons for builtin and registry apps.
// Each icon is a 16x16 viewBox SVG rendered inline.
// Usage: <AppIcon id="terminal" size={20} />

// Logo URLs for installed apps (shared with AppHub)
export const APP_LOGOS = {
  firefox: 'https://upload.wikimedia.org/wikipedia/commons/a/a0/Firefox_logo%2C_2019.svg',
  thunderbird: 'https://upload.wikimedia.org/wikipedia/commons/e/e1/Thunderbird_Logo%2C_2018.svg',
  gimp: 'https://upload.wikimedia.org/wikipedia/commons/4/45/The_GIMP_icon_-_gnome.svg',
  blender: 'https://upload.wikimedia.org/wikipedia/commons/0/0c/Blender_logo_no_text.svg',
  inkscape: 'https://upload.wikimedia.org/wikipedia/commons/0/0d/Inkscape_Logo.svg',
  libreoffice: 'https://upload.wikimedia.org/wikipedia/commons/0/02/LibreOffice_Logo_Flat.svg',
  vlc: 'https://upload.wikimedia.org/wikipedia/commons/e/e6/VLC_Icon.svg',
  audacity: 'https://upload.wikimedia.org/wikipedia/commons/f/f6/Audacity_Logo.svg',
  kicad: 'https://upload.wikimedia.org/wikipedia/commons/5/59/KiCad-Logo.svg',
  keepassxc: 'https://upload.wikimedia.org/wikipedia/commons/c/c1/KeePassXC_icon.svg',
  filezilla: 'https://upload.wikimedia.org/wikipedia/commons/0/01/FileZilla_logo.svg',
  transmission: 'https://upload.wikimedia.org/wikipedia/commons/6/6d/Transmission_icon.png',
  nginx: 'https://upload.wikimedia.org/wikipedia/commons/c/c5/Nginx_logo.svg',
  grafana: 'https://upload.wikimedia.org/wikipedia/commons/a/a1/Grafana_logo.svg',
  jupyter: 'https://upload.wikimedia.org/wikipedia/commons/3/38/Jupyter_logo.svg',
  gitea: 'https://upload.wikimedia.org/wikipedia/commons/b/bb/Gitea_Logo.svg',
  syncthing: 'https://upload.wikimedia.org/wikipedia/commons/8/83/SyncthingAugworGraphic.png',
}

export const APP_COLORS = {
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
}

// First letter for fallback icons (override if app name starts differently than ID)
export const APP_LETTERS = {
  'sqlite-web': 'S', ttyd: 'T', httpbin: 'H', keepassxc: 'K', obs: 'O',
  vlc: 'V', gimp: 'G', kicad: 'K', freecad: 'F',
}

const icons = {
  terminal: (
    <g>
      <rect x="1" y="2" width="14" height="12" rx="2" fill="none" stroke="currentColor" strokeWidth="1.2" />
      <path d="M4 6l3 2-3 2" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" fill="none" />
      <line x1="9" y1="10" x2="12" y2="10" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
    </g>
  ),
  activity: (
    <g>
      <rect x="1" y="2" width="14" height="12" rx="2" fill="none" stroke="currentColor" strokeWidth="1.2" />
      <polyline points="3,10 5,6 7,9 9,4 11,8 13,5" fill="none" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" strokeLinejoin="round" />
    </g>
  ),
  files: (
    <g>
      <path d="M2 4.5A1.5 1.5 0 013.5 3H6l1.5 1.5H12.5A1.5 1.5 0 0114 6v6a1.5 1.5 0 01-1.5 1.5h-9A1.5 1.5 0 012 12V4.5z" fill="none" stroke="currentColor" strokeWidth="1.2" />
    </g>
  ),
  persona: (
    <g>
      <circle cx="8" cy="8" r="6.5" fill="none" stroke="currentColor" strokeWidth="1.2" />
      <circle cx="8" cy="8" r="2" fill="none" stroke="currentColor" strokeWidth="1.2" />
      <path d="M8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2" stroke="currentColor" strokeWidth="1" strokeLinecap="round" />
    </g>
  ),
  browser: (
    <g>
      <circle cx="8" cy="8" r="7" fill="none" stroke="currentColor" strokeWidth="1.2" />
      <circle cx="8" cy="8" r="3" fill="none" stroke="currentColor" strokeWidth="1.2" />
      <path d="M8 5L13.5 5" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
      <path d="M5.5 13L8 8" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
      <path d="M2.5 5L5.5 10" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
    </g>
  ),
  apphub: (
    <g>
      <rect x="2" y="2" width="5" height="5" rx="1" fill="currentColor" opacity="0.7" />
      <rect x="9" y="2" width="5" height="5" rx="1" fill="currentColor" opacity="0.5" />
      <rect x="2" y="9" width="5" height="5" rx="1" fill="currentColor" opacity="0.5" />
      <rect x="9" y="9" width="5" height="5" rx="1" fill="currentColor" opacity="0.3" />
      <line x1="11.5" y1="9.5" x2="11.5" y2="13.5" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
      <line x1="9.5" y1="11.5" x2="13.5" y2="11.5" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
    </g>
  ),
  library: (
    <g>
      <rect x="3" y="2" width="10" height="12" rx="1" fill="none" stroke="currentColor" strokeWidth="1.2" />
      <line x1="5" y1="5" x2="11" y2="5" stroke="currentColor" strokeWidth="1" strokeLinecap="round" />
      <line x1="5" y1="7.5" x2="11" y2="7.5" stroke="currentColor" strokeWidth="1" strokeLinecap="round" />
      <line x1="5" y1="10" x2="9" y2="10" stroke="currentColor" strokeWidth="1" strokeLinecap="round" />
    </g>
  ),
  gallery: (
    <g>
      <rect x="1.5" y="2.5" width="13" height="11" rx="1.5" fill="none" stroke="currentColor" strokeWidth="1.2" />
      <circle cx="5" cy="6" r="1.5" fill="currentColor" opacity="0.6" />
      <path d="M1.5 11l3.5-4 2.5 3 2-1.5L14.5 13" stroke="currentColor" strokeWidth="1" strokeLinejoin="round" fill="none" />
    </g>
  ),
  disks: (
    <g>
      <circle cx="8" cy="8" r="6.5" fill="none" stroke="currentColor" strokeWidth="1.2" />
      <path d="M8 1.5 A6.5 6.5 0 1 1 1.5 8 L8 8 Z" fill="currentColor" opacity="0.5" />
      <circle cx="8" cy="8" r="2" fill="none" stroke="currentColor" strokeWidth="0.8" />
    </g>
  ),
  packages: (
    <g>
      <rect x="2.5" y="1.5" width="11" height="13" rx="1.5" fill="none" stroke="currentColor" strokeWidth="1.2" />
      <path d="M6 1.5V4h4V1.5" stroke="currentColor" strokeWidth="1" fill="none" />
      <line x1="5" y1="7" x2="11" y2="7" stroke="currentColor" strokeWidth="0.8" strokeLinecap="round" />
      <line x1="5" y1="9.5" x2="11" y2="9.5" stroke="currentColor" strokeWidth="0.8" strokeLinecap="round" />
      <line x1="5" y1="12" x2="9" y2="12" stroke="currentColor" strokeWidth="0.8" strokeLinecap="round" />
    </g>
  ),
  drivers: (
    <g>
      <rect x="2" y="3" width="12" height="10" rx="1.5" fill="none" stroke="currentColor" strokeWidth="1.2" />
      <circle cx="5" cy="8" r="1.5" fill="currentColor" opacity="0.6" />
      <circle cx="8" cy="8" r="1.5" fill="currentColor" opacity="0.6" />
      <circle cx="11" cy="8" r="1.5" fill="currentColor" opacity="0.6" />
      <line x1="4" y1="5.5" x2="12" y2="5.5" stroke="currentColor" strokeWidth="0.8" />
    </g>
  ),
  chat: (
    <g>
      <path d="M2 3a2 2 0 012-2h8a2 2 0 012 2v6a2 2 0 01-2 2H6l-3 3V11H4a2 2 0 01-2-2V3z" fill="currentColor" opacity="0.6" />
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

// For dock/launchpad tiles — renders in a rounded square container
// Shows: builtin SVG icon > logo URL from registry > letter fallback
export function AppIconTile({ id, size = 48, unicode }) {
  const icon = icons[id]
  const logo = APP_LOGOS[id]
  const color = APP_COLORS[id]
  const [logoFailed, setLogoFailed] = useState(false)
  const radius = size * 0.28

  // Builtin SVG icon
  if (icon) {
    return (
      <div style={{
        width: size, height: size, borderRadius: radius,
        background: '#1a1a1a', border: '1px solid #2a2a2a',
        display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#d4d4d4',
      }}>
        <svg viewBox="0 0 16 16" width={size * 0.5} height={size * 0.5} fill="none" style={{ color: '#d4d4d4' }}>
          {icon}
        </svg>
      </div>
    )
  }

  // Logo URL from registry
  if (logo && !logoFailed) {
    return (
      <div style={{
        width: size, height: size, borderRadius: radius,
        background: color ? `${color}15` : '#1a1a1a',
        border: `1px solid ${color ? color + '30' : '#2a2a2a'}`,
        display: 'flex', alignItems: 'center', justifyContent: 'center', overflow: 'hidden',
      }}>
        <img
          src={logo}
          alt=""
          style={{ width: '70%', height: '70%', objectFit: 'contain' }}
          onError={() => setLogoFailed(true)}
          loading="lazy"
        />
      </div>
    )
  }

  // Letter fallback — single character with brand color
  const letter = APP_LETTERS[id] || id?.[0]?.toUpperCase() || '?'
  return (
    <div style={{
      width: size, height: size, borderRadius: radius,
      background: color ? `linear-gradient(135deg, ${color}40, ${color}20)` : '#262626',
      border: `1px solid ${color ? color + '30' : '#333'}`,
      display: 'flex', alignItems: 'center', justifyContent: 'center',
      color: color || '#a3a3a3', fontWeight: 700, fontSize: size * 0.38,
    }}>
      {letter}
    </div>
  )
}
