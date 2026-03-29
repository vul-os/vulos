import { useState, useEffect } from 'react'

const CATEGORY_LABELS = {
  all: 'All',
  database: 'Database',
  developer: 'Developer',
  network: 'Network',
  productivity: 'Productivity',
  media: 'Media',
  system: 'System',
  other: 'Other',
}

export default function AppHub() {
  const [apps, setApps] = useState([])
  const [installed, setInstalled] = useState([])
  const [loading, setLoading] = useState(true)
  const [installing, setInstalling] = useState(null)
  const [search, setSearch] = useState('')
  const [category, setCategory] = useState('all')
  const [selectedApp, setSelectedApp] = useState(null)
  const [selectedVersion, setSelectedVersion] = useState('')
  const [tab, setTab] = useState('browse') // 'browse' | 'installed'
  const [error, setError] = useState(null)
  const [success, setSuccess] = useState(null)

  const fetchData = async () => {
    setLoading(true)
    try {
      const [regRes, instRes] = await Promise.all([
        fetch('/api/store/registry'),
        fetch('/api/store/installed'),
      ])
      const regData = await regRes.json()
      const instData = await instRes.json()
      setApps(regData || [])
      setInstalled(instData || [])
    } catch {
      setApps([])
      setInstalled([])
    }
    setLoading(false)
  }

  useEffect(() => { fetchData() }, [])

  const installApp = async (appId, version) => {
    setInstalling(appId)
    setError(null)
    setSuccess(null)
    try {
      const res = await fetch('/api/store/registry/install', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ app_id: appId, version: version || '' }),
      })
      if (!res.ok) {
        const data = await res.json()
        throw new Error(data.error || 'Install failed')
      }
      setSuccess(`${appId} installed`)
      setTimeout(() => setSuccess(null), 3000)
      await fetchData()
    } catch (e) {
      setError(e.message)
      setTimeout(() => setError(null), 5000)
    }
    setInstalling(null)
  }

  const uninstallApp = async (appId) => {
    setInstalling(appId)
    setError(null)
    try {
      const res = await fetch('/api/store/uninstall', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ app_id: appId }),
      })
      if (!res.ok) throw new Error('Uninstall failed')
      setSuccess(`${appId} removed`)
      setTimeout(() => setSuccess(null), 3000)
      await fetchData()
      if (selectedApp?.id === appId) setSelectedApp(null)
    } catch (e) {
      setError(e.message)
      setTimeout(() => setError(null), 5000)
    }
    setInstalling(null)
  }

  const filtered = apps.filter(app => {
    if (category !== 'all' && app.category !== category) return false
    if (search.trim()) {
      const q = search.toLowerCase()
      return app.name.toLowerCase().includes(q) ||
        app.description.toLowerCase().includes(q) ||
        app.id.toLowerCase().includes(q) ||
        (app.versions || []).some(v => v.includes(q))
    }
    return true
  })

  const categories = ['all', ...new Set(apps.map(a => a.category).filter(Boolean))]
  const installedIds = new Set((installed || []).map(a => a.id))

  const browseList = tab === 'installed' ? filtered.filter(a => a.installed || installedIds.has(a.id)) : filtered

  return (
    <div style={st.root}>
      {/* Header */}
      <div style={st.header}>
        <span style={st.title}>App Hub</span>
        <div style={st.tabs}>
          <button style={{ ...st.tab, ...(tab === 'browse' ? st.tabActive : {}) }} onClick={() => setTab('browse')}>Browse</button>
          <button style={{ ...st.tab, ...(tab === 'installed' ? st.tabActive : {}) }} onClick={() => setTab('installed')}>
            Installed ({installed?.length || 0})
          </button>
        </div>
      </div>

      {/* Toast */}
      {error && <div style={st.toast.error}>{error}</div>}
      {success && <div style={st.toast.success}>{success}</div>}

      {/* Search + filters */}
      <div style={st.filterBar}>
        <input
          value={search}
          onChange={e => setSearch(e.target.value)}
          placeholder="Search apps..."
          style={st.searchInput}
        />
        <div style={st.categoryScroll}>
          {categories.map(cat => (
            <button
              key={cat}
              style={{ ...st.catBtn, ...(category === cat ? st.catBtnActive : {}) }}
              onClick={() => setCategory(cat)}
            >
              {CATEGORY_LABELS[cat] || cat}
            </button>
          ))}
        </div>
      </div>

      <div style={st.body}>
        {/* App grid */}
        <div style={st.grid}>
          {loading ? (
            <div style={st.empty}>Loading registry...</div>
          ) : browseList.length === 0 ? (
            <div style={st.empty}>{tab === 'installed' ? 'No apps installed' : 'No apps found'}</div>
          ) : (
            browseList.map(app => {
              const isInstalled = app.installed || installedIds.has(app.id)
              const isInstalling = installing === app.id
              const isSelected = selectedApp?.id === app.id

              return (
                <div
                  key={app.id}
                  style={{ ...st.card, ...(isSelected ? st.cardSelected : {}) }}
                  onClick={() => { setSelectedApp(app); setSelectedVersion(app.latest || '') }}
                >
                  <div style={st.cardTop}>
                    <div style={st.cardIcon}>{app.icon || app.name?.[0]?.toUpperCase() || '?'}</div>
                    <div style={st.cardInfo}>
                      <div style={st.cardName}>
                        {app.name}
                        {app.vetted && <span style={st.vettedBadge} title="Vetted by Vula OS">{'\u2713'}</span>}
                      </div>
                      <div style={st.cardDesc}>{app.description}</div>
                    </div>
                  </div>

                  <div style={st.cardBottom}>
                    <div style={st.cardMeta}>
                      {app.category && <span style={st.metaTag}>{app.category}</span>}
                      {app.latest && <span style={st.metaVersion}>{app.latest}</span>}
                    </div>
                    {isInstalled ? (
                      <span style={st.installedBadge}>Installed</span>
                    ) : (
                      <button
                        style={st.installBtn}
                        onClick={(e) => { e.stopPropagation(); installApp(app.id, app.latest) }}
                        disabled={isInstalling}
                      >
                        {isInstalling ? '...' : 'Install'}
                      </button>
                    )}
                  </div>
                </div>
              )
            })
          )}
        </div>

        {/* Detail panel */}
        {selectedApp && (
          <div style={st.detail}>
            <div style={st.detailHeader}>
              <div style={st.detailIcon}>{selectedApp.icon || '?'}</div>
              <div>
                <div style={st.detailName}>
                  {selectedApp.name}
                  {selectedApp.vetted && <span style={st.vettedBadge}>{'\u2713'}</span>}
                </div>
                <div style={st.detailAuthor}>{selectedApp.author || 'Unknown'}</div>
              </div>
              <button style={st.detailClose} onClick={() => setSelectedApp(null)}>{'\u00D7'}</button>
            </div>

            <div style={st.detailBody}>
              <div style={st.detailDesc}>{selectedApp.description}</div>

              <div style={st.detailSection}>
                <div style={st.detailLabel}>Version</div>
                <div style={st.versionPicker}>
                  {(selectedApp.versions || []).map(v => (
                    <button
                      key={v}
                      style={{ ...st.versionBtn, ...(selectedVersion === v ? st.versionBtnActive : {}) }}
                      onClick={() => setSelectedVersion(v)}
                    >
                      {v}
                    </button>
                  ))}
                </div>
              </div>

              <div style={st.detailSection}>
                <div style={st.detailLabel}>Details</div>
                <div style={st.detailMeta}>
                  <MetaRow label="Category" value={CATEGORY_LABELS[selectedApp.category] || selectedApp.category} />
                  <MetaRow label="License" value={selectedApp.license || '\u2014'} />
                  <MetaRow label="ID" value={selectedApp.id} />
                  {selectedApp.homepage && <MetaRow label="Homepage" value={selectedApp.homepage} />}
                </div>
              </div>

              <div style={st.detailActions}>
                {selectedApp.installed || installedIds.has(selectedApp.id) ? (
                  <>
                    <span style={{ ...st.installedBadge, fontSize: 12 }}>Installed</span>
                    <button
                      style={st.removeBtn}
                      onClick={() => uninstallApp(selectedApp.id)}
                      disabled={installing === selectedApp.id}
                    >
                      {installing === selectedApp.id ? 'Removing...' : 'Remove'}
                    </button>
                  </>
                ) : (
                  <button
                    style={st.installBtnLarge}
                    onClick={() => installApp(selectedApp.id, selectedVersion)}
                    disabled={installing === selectedApp.id}
                  >
                    {installing === selectedApp.id ? 'Installing...' : `Install ${selectedVersion || ''}`}
                  </button>
                )}
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function MetaRow({ label, value }) {
  return (
    <div style={{ display: 'flex', justifyContent: 'space-between', padding: '3px 0', fontSize: 11 }}>
      <span style={{ color: 'var(--text-ghost)' }}>{label}</span>
      <span style={{ color: 'var(--text-tertiary)', maxWidth: 180, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{value}</span>
    </div>
  )
}

const st = {
  root: {
    display: 'flex',
    flexDirection: 'column',
    height: '100%',
    background: 'var(--bg-base)',
    color: 'var(--text-secondary)',
    fontSize: 12,
    overflow: 'hidden',
  },

  header: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: '10px 14px',
    borderBottom: '1px solid var(--border-default)',
    flexShrink: 0,
  },
  title: { fontSize: 14, fontWeight: 600, color: 'var(--text-primary)' },
  tabs: { display: 'flex', gap: 2, background: 'var(--bg-surface)', borderRadius: 6, padding: 2 },
  tab: {
    background: 'none',
    border: 'none',
    color: 'var(--text-faint)',
    fontSize: 11,
    padding: '4px 10px',
    borderRadius: 4,
    cursor: 'pointer',
  },
  tabActive: { background: 'var(--bg-hover)', color: 'var(--text-secondary)' },

  toast: {
    error: {
      margin: '8px 14px 0',
      padding: '6px 10px',
      background: '#2a1215',
      border: '1px solid #5c2028',
      borderRadius: 6,
      fontSize: 11,
      color: '#f87171',
    },
    success: {
      margin: '8px 14px 0',
      padding: '6px 10px',
      background: '#0f2a1a',
      border: '1px solid #1a5c32',
      borderRadius: 6,
      fontSize: 11,
      color: '#4ade80',
    },
  },

  filterBar: {
    padding: '8px 14px',
    borderBottom: '1px solid var(--border-subtle)',
    display: 'flex',
    flexDirection: 'column',
    gap: 6,
    flexShrink: 0,
  },
  searchInput: {
    width: '100%',
    background: 'var(--bg-surface)',
    border: '1px solid var(--border-default)',
    borderRadius: 6,
    padding: '6px 10px',
    color: 'var(--text-secondary)',
    fontSize: 12,
    outline: 'none',
    fontFamily: 'inherit',
  },
  categoryScroll: {
    display: 'flex',
    gap: 4,
    overflowX: 'auto',
  },
  catBtn: {
    background: 'none',
    border: '1px solid var(--border-default)',
    borderRadius: 4,
    color: 'var(--text-ghost)',
    fontSize: 10,
    padding: '3px 8px',
    cursor: 'pointer',
    whiteSpace: 'nowrap',
    flexShrink: 0,
  },
  catBtnActive: {
    background: 'var(--bg-hover)',
    color: 'var(--text-secondary)',
    borderColor: 'var(--border-emphasis)',
  },

  body: {
    flex: 1,
    display: 'flex',
    minHeight: 0,
    overflow: 'hidden',
  },

  grid: {
    flex: 1,
    overflowY: 'auto',
    padding: 10,
    display: 'flex',
    flexDirection: 'column',
    gap: 6,
  },
  empty: {
    padding: 40,
    textAlign: 'center',
    color: 'var(--text-invisible)',
    fontSize: 12,
  },

  card: {
    display: 'flex',
    flexDirection: 'column',
    gap: 8,
    padding: 12,
    background: 'var(--bg-surface)',
    border: '1px solid var(--border-default)',
    borderRadius: 8,
    cursor: 'pointer',
  },
  cardSelected: {
    borderColor: 'var(--border-emphasis)',
    background: 'var(--bg-selected)',
  },
  cardTop: {
    display: 'flex',
    gap: 10,
    alignItems: 'flex-start',
  },
  cardIcon: {
    width: 38,
    height: 38,
    borderRadius: 8,
    background: 'var(--bg-elevated)',
    border: '1px solid var(--border-strong)',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    fontSize: 18,
    flexShrink: 0,
  },
  cardInfo: {
    flex: 1,
    minWidth: 0,
  },
  cardName: {
    fontSize: 13,
    fontWeight: 500,
    color: 'var(--text-primary)',
    display: 'flex',
    alignItems: 'center',
    gap: 5,
  },
  cardDesc: {
    fontSize: 11,
    color: 'var(--text-faint)',
    marginTop: 2,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  cardBottom: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  cardMeta: {
    display: 'flex',
    gap: 6,
    alignItems: 'center',
  },
  metaTag: {
    fontSize: 9,
    color: 'var(--text-ghost)',
    background: 'var(--bg-elevated)',
    padding: '2px 6px',
    borderRadius: 3,
    textTransform: 'uppercase',
    letterSpacing: '0.03em',
  },
  metaVersion: {
    fontSize: 10,
    color: 'var(--text-dim)',
  },
  vettedBadge: {
    fontSize: 9,
    color: '#22c55e',
    background: '#0f2a1a',
    borderRadius: 3,
    padding: '1px 4px',
  },
  installedBadge: {
    fontSize: 10,
    color: '#3b82f6',
    background: '#111d33',
    padding: '3px 8px',
    borderRadius: 4,
  },
  installBtn: {
    background: 'var(--bg-hover)',
    border: '1px solid var(--border-emphasis)',
    borderRadius: 5,
    color: 'var(--text-secondary)',
    fontSize: 11,
    padding: '4px 12px',
    cursor: 'pointer',
  },

  // Detail panel
  detail: {
    width: 280,
    borderLeft: '1px solid var(--border-default)',
    display: 'flex',
    flexDirection: 'column',
    flexShrink: 0,
    overflow: 'hidden',
    background: 'var(--bg-base)',
  },
  detailHeader: {
    display: 'flex',
    gap: 10,
    alignItems: 'center',
    padding: '12px 14px',
    borderBottom: '1px solid var(--border-default)',
    flexShrink: 0,
  },
  detailIcon: {
    width: 44,
    height: 44,
    borderRadius: 10,
    background: 'var(--bg-elevated)',
    border: '1px solid var(--border-strong)',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    fontSize: 22,
    flexShrink: 0,
  },
  detailName: {
    fontSize: 14,
    fontWeight: 600,
    color: 'var(--text-primary)',
    display: 'flex',
    alignItems: 'center',
    gap: 5,
  },
  detailAuthor: {
    fontSize: 11,
    color: 'var(--text-ghost)',
    marginTop: 1,
  },
  detailClose: {
    marginLeft: 'auto',
    background: 'none',
    border: 'none',
    color: 'var(--text-ghost)',
    fontSize: 16,
    cursor: 'pointer',
    padding: '0 4px',
    flexShrink: 0,
  },
  detailBody: {
    flex: 1,
    overflowY: 'auto',
    padding: 14,
    display: 'flex',
    flexDirection: 'column',
    gap: 14,
  },
  detailDesc: {
    fontSize: 12,
    color: 'var(--text-tertiary)',
    lineHeight: 1.5,
  },
  detailSection: {},
  detailLabel: {
    fontSize: 10,
    color: 'var(--text-ghost)',
    textTransform: 'uppercase',
    letterSpacing: '0.05em',
    marginBottom: 6,
  },
  detailMeta: {
    background: 'var(--bg-surface)',
    borderRadius: 6,
    padding: '6px 10px',
    border: '1px solid var(--border-default)',
  },
  versionPicker: {
    display: 'flex',
    gap: 4,
    flexWrap: 'wrap',
  },
  versionBtn: {
    background: 'var(--bg-surface)',
    border: '1px solid var(--border-default)',
    borderRadius: 4,
    color: 'var(--text-muted)',
    fontSize: 11,
    padding: '3px 10px',
    cursor: 'pointer',
  },
  versionBtnActive: {
    background: 'var(--bg-selected)',
    borderColor: '#3b82f6',
    color: 'var(--text-secondary)',
  },
  detailActions: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    marginTop: 4,
  },
  installBtnLarge: {
    flex: 1,
    background: 'var(--bg-hover)',
    border: '1px solid var(--border-emphasis)',
    borderRadius: 6,
    color: 'var(--text-primary)',
    fontSize: 12,
    fontWeight: 500,
    padding: '8px 16px',
    cursor: 'pointer',
    textAlign: 'center',
  },
  removeBtn: {
    background: 'none',
    border: '1px solid #5c2028',
    borderRadius: 6,
    color: '#f87171',
    fontSize: 11,
    padding: '6px 12px',
    cursor: 'pointer',
  },
}
