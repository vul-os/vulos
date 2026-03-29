import { useState, useEffect, useCallback, useRef } from 'react'

const ICON_DIR = '\u25B8'
const ICON_DIR_OPEN = '\u25BE'
const ICON_LINK = '\u2197'

const EXT_ICONS = {
  js: '\u25C7', jsx: '\u25C7', ts: '\u25C7', tsx: '\u25C7', mjs: '\u25C7',
  py: '\u25C8', go: '\u25C8', rs: '\u25C8', c: '\u25C8', cpp: '\u25C8', h: '\u25C8', java: '\u25C8',
  md: '\u2261', txt: '\u2261', log: '\u2261', csv: '\u2261',
  json: '{}', yaml: '{}', yml: '{}', toml: '{}', xml: '{}', ini: '{}',
  sh: '>_', bash: '>_', zsh: '>_', fish: '>_',
  png: '\u25FB', jpg: '\u25FB', jpeg: '\u25FB', gif: '\u25FB', svg: '\u25FB', webp: '\u25FB', ico: '\u25FB',
  mp4: '\u25B7', mov: '\u25B7', avi: '\u25B7', mkv: '\u25B7', webm: '\u25B7',
  mp3: '\u266A', wav: '\u266A', flac: '\u266A', ogg: '\u266A',
  zip: '\u25A4', tar: '\u25A4', gz: '\u25A4', xz: '\u25A4', bz2: '\u25A4', '7z': '\u25A4',
  pdf: '\u25A6', doc: '\u25A6', docx: '\u25A6',
  lock: '\u25C9', env: '\u25C9',
}

const SIDEBAR_PLACES = [
  { label: 'Home', path: '~', icon: '\u2302' },
  { label: 'Desktop', path: '~/Desktop', icon: '\u25A3' },
  { label: 'Documents', path: '~/Documents', icon: '\u2261' },
  { label: 'Downloads', path: '~/Downloads', icon: '\u2193' },
  { label: 'Pictures', path: '~/Pictures', icon: '\u25FB' },
  { label: 'Music', path: '~/Music', icon: '\u266A' },
  { label: 'Videos', path: '~/Videos', icon: '\u25B7' },
]

const SIDEBAR_SYSTEM = [
  { label: 'Root', path: '/', icon: '/' },
  { label: 'Tmp', path: '/tmp', icon: '\u25CC' },
]

function fileIcon(name, isDir, isLink) {
  if (isDir) return ICON_DIR
  if (isLink) return ICON_LINK
  const ext = name.split('.').pop().toLowerCase()
  return EXT_ICONS[ext] || '\u00B7'
}

function fmtSize(b) {
  if (b < 1024) return `${b} B`
  if (b < 1048576) return `${(b / 1024).toFixed(1)} K`
  if (b < 1073741824) return `${(b / 1048576).toFixed(1)} M`
  return `${(b / 1073741824).toFixed(1)} G`
}

function fmtPerms(perms) {
  return perms?.slice(0, 10) || ''
}

async function exec(command) {
  const res = await fetch('/api/exec', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ command }),
  })
  const data = await res.json()
  return data.output || ''
}

export default function FileManager() {
  const [cwd, setCwd] = useState('~')
  const [entries, setEntries] = useState([])
  const [loading, setLoading] = useState(false)
  const [selected, setSelected] = useState(null)
  const [preview, setPreview] = useState(null)
  const [previewLoading, setPreviewLoading] = useState(false)
  const [query, setQuery] = useState('')
  const [searchResults, setSearchResults] = useState(null)
  const [searchMode, setSearchMode] = useState('name')
  const [sortCol, setSortCol] = useState('name')
  const [sortAsc, setSortAsc] = useState(true)
  const [history, setHistory] = useState(['~'])
  const [histIdx, setHistIdx] = useState(0)
  const [pathInput, setPathInput] = useState('')
  const [editingPath, setEditingPath] = useState(false)
  const [hidden, setHidden] = useState(false)
  const [resolvedHome, setResolvedHome] = useState(null)
  const searchRef = useRef(null)

  // Resolve actual home path once
  useEffect(() => {
    exec('echo $HOME').then(out => {
      const h = out.trim()
      if (h) setResolvedHome(h)
    })
  }, [])

  const loadDir = useCallback(async (dir, pushHistory = true) => {
    setLoading(true)
    setSearchResults(null)
    setSelected(null)
    setPreview(null)
    try {
      const flag = hidden ? '-la' : '-lA'
      const out = await exec(`ls ${flag} --color=never "${dir}" 2>/dev/null | tail -n +2`)
      const lines = out.split('\n').filter(l => l.trim())
      const parsed = lines.map(line => {
        const parts = line.split(/\s+/)
        if (parts.length < 9) return null
        const perms = parts[0]
        const links = parts[1]
        const owner = parts[2]
        const group = parts[3]
        const size = parseInt(parts[4]) || 0
        const name = parts.slice(8).join(' ')
        if (name === '.' || name === '..') return null
        const linkTarget = name.includes(' -> ') ? name.split(' -> ')[1] : null
        const cleanName = name.includes(' -> ') ? name.split(' -> ')[0] : name
        return {
          name: cleanName,
          size,
          perms,
          links: parseInt(links) || 0,
          owner,
          group,
          isDir: perms.startsWith('d'),
          isLink: perms.startsWith('l'),
          linkTarget,
          modified: `${parts[5]} ${parts[6]} ${parts[7]}`,
        }
      }).filter(Boolean)
      setEntries(parsed)
      setCwd(dir)
      setPathInput(dir)
      if (pushHistory) {
        setHistory(prev => [...prev.slice(0, histIdx + 1), dir])
        setHistIdx(prev => prev + 1)
      }
    } catch {
      setEntries([])
    }
    setLoading(false)
  }, [hidden, histIdx])

  useEffect(() => { loadDir('~', true) }, [])

  const navigate = (name) => {
    const target = cwd === '/' ? `/${name}` : `${cwd}/${name}`
    loadDir(target)
  }

  const goUp = () => {
    if (cwd === '/') return
    const parent = cwd.includes('/') ? cwd.split('/').slice(0, -1).join('/') || '/' : '~'
    loadDir(parent)
  }

  const goBack = () => {
    if (histIdx <= 0) return
    const newIdx = histIdx - 1
    setHistIdx(newIdx)
    loadDir(history[newIdx], false)
  }

  const goForward = () => {
    if (histIdx >= history.length - 1) return
    const newIdx = histIdx + 1
    setHistIdx(newIdx)
    loadDir(history[newIdx], false)
  }

  const handlePathSubmit = (e) => {
    e.preventDefault()
    setEditingPath(false)
    if (pathInput.trim()) loadDir(pathInput.trim())
  }

  const doSearch = async () => {
    if (!query.trim()) { setSearchResults(null); return }

    if (searchMode === 'semantic') {
      try {
        const res = await fetch('/api/recall/search', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ query, top_k: 30 }),
        })
        const results = await res.json()
        setSearchResults({ type: 'semantic', items: results || [] })
      } catch {
        setSearchResults({ type: 'semantic', items: [] })
      }
    } else {
      try {
        const out = await exec(`find "${cwd}" -maxdepth 3 -iname "*${query.replace(/"/g, '')}*" 2>/dev/null | head -50`)
        const paths = out.split('\n').filter(l => l.trim())
        setSearchResults({
          type: 'name',
          items: paths.map(p => ({ path: p, name: p.split('/').pop() })),
        })
      } catch {
        setSearchResults({ type: 'name', items: [] })
      }
    }
  }

  const selectEntry = async (entry, idx) => {
    setSelected(idx)
    if (entry.isDir) {
      setPreview({ type: 'dir', name: entry.name, entry })
      return
    }
    const filePath = cwd === '/' ? `/${entry.name}` : `${cwd}/${entry.name}`
    const ext = entry.name.split('.').pop().toLowerCase()
    const imageExts = ['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'ico']

    if (imageExts.includes(ext)) {
      setPreview({ type: 'image', path: filePath, name: entry.name, entry })
      return
    }

    setPreviewLoading(true)
    try {
      const content = await exec(`head -80 "${filePath}" 2>/dev/null`)
      setPreview({ type: 'text', content: content || '(empty)', name: entry.name, path: filePath, entry })
    } catch {
      setPreview({ type: 'text', content: '(could not read)', name: entry.name, path: filePath, entry })
    }
    setPreviewLoading(false)
  }

  const sorted = [...entries].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    let cmp = 0
    if (sortCol === 'name') cmp = a.name.localeCompare(b.name)
    else if (sortCol === 'size') cmp = a.size - b.size
    else if (sortCol === 'modified') cmp = a.modified.localeCompare(b.modified)
    else if (sortCol === 'perms') cmp = a.perms.localeCompare(b.perms)
    return sortAsc ? cmp : -cmp
  })

  const toggleSort = (col) => {
    if (sortCol === col) setSortAsc(!sortAsc)
    else { setSortCol(col); setSortAsc(true) }
  }

  const sortArrow = (col) => sortCol === col ? (sortAsc ? ' \u2191' : ' \u2193') : ''

  // Check which sidebar item is active
  const isActive = (place) => {
    if (place.path === '~' && (cwd === '~' || cwd === resolvedHome)) return true
    if (place.path.startsWith('~/') && resolvedHome) {
      const expanded = resolvedHome + place.path.slice(1)
      return cwd === place.path || cwd === expanded
    }
    return cwd === place.path
  }

  const breadcrumbs = cwd === '~'
    ? [{ label: '~', path: '~' }]
    : cwd.split('/').reduce((acc, part, i) => {
        if (!part && i === 0) {
          acc.push({ label: '/', path: '/' })
        } else if (part) {
          const prev = acc.length > 0 ? acc[acc.length - 1].path : ''
          acc.push({ label: part, path: prev === '/' ? `/${part}` : `${prev}/${part}` })
        }
        return acc
      }, [])

  useEffect(() => {
    const handler = (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'f') {
        e.preventDefault()
        searchRef.current?.focus()
      }
      if (e.key === 'Backspace' && !e.target.closest('input')) {
        e.preventDefault()
        goUp()
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [cwd])

  return (
    <div style={s.root}>
      {/* Toolbar */}
      <div style={s.toolbar}>
        <div style={s.navBtns}>
          <button style={s.navBtn} onClick={goBack} disabled={histIdx <= 0} title="Back">{'\u2190'}</button>
          <button style={s.navBtn} onClick={goForward} disabled={histIdx >= history.length - 1} title="Forward">{'\u2192'}</button>
          <button style={s.navBtn} onClick={goUp} disabled={cwd === '/'} title="Up">{'\u2191'}</button>
        </div>

        {/* Breadcrumb / path input */}
        <div style={s.pathBar} onClick={() => setEditingPath(true)}>
          {editingPath ? (
            <form onSubmit={handlePathSubmit} style={{ display: 'contents' }}>
              <input
                autoFocus
                value={pathInput}
                onChange={e => setPathInput(e.target.value)}
                onBlur={() => setEditingPath(false)}
                style={s.pathInput}
              />
            </form>
          ) : (
            <div style={s.breadcrumbs}>
              {breadcrumbs.map((b, i) => (
                <span key={i} style={s.crumb}>
                  {i > 0 && <span style={{ color: 'var(--text-invisible)', margin: '0 2px' }}>/</span>}
                  <span
                    style={s.crumbLabel}
                    onClick={(e) => { e.stopPropagation(); loadDir(b.path) }}
                  >
                    {b.label}
                  </span>
                </span>
              ))}
            </div>
          )}
        </div>

        {/* Search inline */}
        <div style={s.toolbarSearch}>
          <span style={{ color: 'var(--text-ghost)', fontSize: 11, flexShrink: 0 }}>{'\u2315'}</span>
          <input
            ref={searchRef}
            value={query}
            onChange={e => setQuery(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') doSearch(); if (e.key === 'Escape') { setQuery(''); setSearchResults(null) } }}
            placeholder="Search..."
            style={s.searchInput}
          />
          {query && (
            <button style={s.clearBtn} onClick={() => { setQuery(''); setSearchResults(null) }}>{'\u00D7'}</button>
          )}
        </div>

        <button
          style={{ ...s.navBtn, fontSize: 10, opacity: hidden ? 1 : 0.4 }}
          onClick={() => { setHidden(!hidden); loadDir(cwd, false) }}
          title="Show hidden files"
        >
          .*
        </button>
        <button style={s.navBtn} onClick={() => loadDir(cwd, false)} title="Refresh">{'\u21BB'}</button>
      </div>

      <div style={s.body}>
        {/* Sidebar */}
        <div style={s.sidebar}>
          <div style={s.sidebarSection}>
            <div style={s.sidebarLabel}>Places</div>
            {SIDEBAR_PLACES.map(place => (
              <button
                key={place.path}
                style={{ ...s.sidebarItem, ...(isActive(place) ? s.sidebarItemActive : {}) }}
                onClick={() => loadDir(place.path)}
              >
                <span style={s.sidebarIcon}>{place.icon}</span>
                <span>{place.label}</span>
              </button>
            ))}
          </div>

          <div style={s.sidebarSection}>
            <div style={s.sidebarLabel}>System</div>
            {SIDEBAR_SYSTEM.map(place => (
              <button
                key={place.path}
                style={{ ...s.sidebarItem, ...(isActive(place) ? s.sidebarItemActive : {}) }}
                onClick={() => loadDir(place.path)}
              >
                <span style={s.sidebarIcon}>{place.icon}</span>
                <span>{place.label}</span>
              </button>
            ))}
          </div>
        </div>

        {/* Main content area */}
        <div style={s.mainArea}>
          {/* Column headers */}
          <div style={s.colHeader}>
            <span style={{ ...s.colName, cursor: 'pointer' }} onClick={() => toggleSort('name')}>
              Name{sortArrow('name')}
            </span>
            <span style={{ ...s.colSize, cursor: 'pointer' }} onClick={() => toggleSort('size')}>
              Size{sortArrow('size')}
            </span>
            <span style={{ ...s.colMod, cursor: 'pointer' }} onClick={() => toggleSort('modified')}>
              Modified{sortArrow('modified')}
            </span>
          </div>

          {/* File rows */}
          <div style={s.listScroll}>
            {loading && <div style={s.empty}>Loading...</div>}

            {searchResults ? (
              searchResults.items.length === 0 ? (
                <div style={s.empty}>No results</div>
              ) : searchResults.type === 'semantic' ? (
                searchResults.items.map((r, i) => {
                  const p = r.metadata?.abs_path || r.metadata?.path || ''
                  const name = p.split('/').pop()
                  return (
                    <div
                      key={i}
                      style={{ ...s.row, ...(selected === `s${i}` ? s.rowSelected : {}) }}
                      onClick={() => { setSelected(`s${i}`); setPreview({ type: 'semantic', name, path: p, score: r.score, content: r.content }) }}
                      onDoubleClick={() => {
                        const dir = p.split('/').slice(0, -1).join('/')
                        if (dir) loadDir(dir)
                      }}
                    >
                      <span style={s.colName}>
                        <span style={{ ...s.icon, color: '#a855f7' }}>{'\u25C8'}</span>
                        <span style={s.fileName}>{name}</span>
                        <span style={s.matchScore}>{Math.round((r.score || 0) * 100)}%</span>
                      </span>
                      <span style={s.colSize}>{'\u2014'}</span>
                      <span style={s.colMod}>
                        <span style={{ color: 'var(--text-dim)', fontSize: 10 }}>{p.split('/').slice(0, -1).join('/')}</span>
                      </span>
                    </div>
                  )
                })
              ) : (
                searchResults.items.map((r, i) => (
                  <div
                    key={i}
                    style={{ ...s.row, ...(selected === `f${i}` ? s.rowSelected : {}) }}
                    onClick={() => setSelected(`f${i}`)}
                    onDoubleClick={() => {
                      const dir = r.path.split('/').slice(0, -1).join('/')
                      if (dir) loadDir(dir)
                    }}
                  >
                    <span style={s.colName}>
                      <span style={s.icon}>{fileIcon(r.name, false, false)}</span>
                      <span style={s.fileName}>{r.name}</span>
                    </span>
                    <span style={s.colSize}>{'\u2014'}</span>
                    <span style={s.colMod}>
                      <span style={{ color: 'var(--text-dim)', fontSize: 10 }}>{r.path}</span>
                    </span>
                  </div>
                ))
              )
            ) : (
              sorted.map((entry, i) => (
                <div
                  key={entry.name}
                  style={{ ...s.row, ...(selected === i ? s.rowSelected : {}) }}
                  onClick={() => selectEntry(entry, i)}
                  onDoubleClick={() => entry.isDir && navigate(entry.name)}
                >
                  <span style={s.colName}>
                    <span style={{ ...s.icon, color: entry.isDir ? '#3b82f6' : entry.isLink ? '#22d3ee' : 'var(--text-ghost)' }}>
                      {fileIcon(entry.name, entry.isDir, entry.isLink)}
                    </span>
                    <span style={{ ...s.fileName, fontWeight: entry.isDir ? 500 : 400 }}>
                      {entry.name}
                    </span>
                    {entry.linkTarget && <span style={s.linkTarget}>{'\u2192'} {entry.linkTarget}</span>}
                  </span>
                  <span style={s.colSize}>{entry.isDir ? '\u2014' : fmtSize(entry.size)}</span>
                  <span style={s.colMod}>{entry.modified}</span>
                </div>
              ))
            )}

            {!loading && !searchResults && entries.length === 0 && (
              <div style={s.empty}>Empty directory</div>
            )}
          </div>
        </div>

        {/* Preview pane */}
        {preview && (
          <div style={s.previewPane}>
            <div style={s.previewHeader}>
              <span style={s.previewTitle}>{preview.name}</span>
              <button style={s.previewClose} onClick={() => { setPreview(null); setSelected(null) }}>{'\u00D7'}</button>
            </div>

            {preview.entry && (
              <div style={s.previewMeta}>
                {preview.entry.perms && <div style={s.metaRow}><span style={s.metaLabel}>Permissions</span><span>{preview.entry.perms}</span></div>}
                {preview.entry.owner && <div style={s.metaRow}><span style={s.metaLabel}>Owner</span><span>{preview.entry.owner}:{preview.entry.group}</span></div>}
                {preview.entry.size !== undefined && !preview.entry.isDir && <div style={s.metaRow}><span style={s.metaLabel}>Size</span><span>{fmtSize(preview.entry.size)}</span></div>}
                {preview.entry.modified && <div style={s.metaRow}><span style={s.metaLabel}>Modified</span><span>{preview.entry.modified}</span></div>}
                {preview.entry.linkTarget && <div style={s.metaRow}><span style={s.metaLabel}>Link</span><span>{preview.entry.linkTarget}</span></div>}
              </div>
            )}

            {preview.type === 'semantic' && (
              <div style={s.previewMeta}>
                <div style={s.metaRow}><span style={s.metaLabel}>Match</span><span>{Math.round((preview.score || 0) * 100)}%</span></div>
                <div style={s.metaRow}><span style={s.metaLabel}>Path</span><span style={{ fontSize: 10 }}>{preview.path}</span></div>
              </div>
            )}

            <div style={s.previewBody}>
              {previewLoading ? (
                <span style={{ color: 'var(--text-dim)' }}>Loading...</span>
              ) : preview.type === 'dir' ? (
                <div style={{ color: 'var(--text-ghost)', fontSize: 12, padding: 12 }}>
                  Double-click to open directory
                </div>
              ) : preview.type === 'image' ? (
                <div style={{ color: 'var(--text-ghost)', fontSize: 12, padding: 12 }}>
                  Image: {preview.name}
                </div>
              ) : (
                <pre style={s.previewCode}>{preview.content}</pre>
              )}
            </div>
          </div>
        )}
      </div>

      {/* Status bar */}
      <div style={s.statusBar}>
        <span>{entries.length} items</span>
        <span>{cwd}</span>
      </div>
    </div>
  )
}

const s = {
  root: {
    display: 'flex',
    flexDirection: 'column',
    height: '100%',
    background: 'var(--bg-base)',
    color: 'var(--text-secondary)',
    fontSize: 12,
    overflow: 'hidden',
  },

  // Toolbar
  toolbar: {
    display: 'flex',
    alignItems: 'center',
    gap: 6,
    padding: '6px 8px',
    background: 'var(--bg-surface)',
    borderBottom: '1px solid var(--border-default)',
    flexShrink: 0,
  },
  navBtns: {
    display: 'flex',
    gap: 2,
  },
  navBtn: {
    background: 'none',
    border: '1px solid var(--border-strong)',
    borderRadius: 4,
    color: 'var(--text-muted)',
    fontSize: 12,
    width: 26,
    height: 24,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    cursor: 'pointer',
  },
  pathBar: {
    flex: 1,
    background: 'var(--bg-base)',
    border: '1px solid var(--border-default)',
    borderRadius: 5,
    padding: '4px 8px',
    minWidth: 0,
    cursor: 'text',
  },
  pathInput: {
    width: '100%',
    background: 'none',
    border: 'none',
    outline: 'none',
    color: 'var(--text-primary)',
    fontSize: 12,
    fontFamily: 'inherit',
  },
  breadcrumbs: {
    display: 'flex',
    alignItems: 'center',
    overflow: 'hidden',
    whiteSpace: 'nowrap',
  },
  crumb: {
    display: 'inline-flex',
    alignItems: 'center',
  },
  crumbLabel: {
    color: 'var(--text-muted)',
    cursor: 'pointer',
    padding: '0 1px',
  },
  toolbarSearch: {
    display: 'flex',
    alignItems: 'center',
    gap: 4,
    background: 'var(--bg-base)',
    border: '1px solid var(--border-default)',
    borderRadius: 5,
    padding: '3px 8px',
    width: 160,
    flexShrink: 0,
  },
  searchInput: {
    flex: 1,
    background: 'none',
    border: 'none',
    outline: 'none',
    color: 'var(--text-secondary)',
    fontSize: 11,
    fontFamily: 'inherit',
    minWidth: 0,
  },
  clearBtn: {
    background: 'none',
    border: 'none',
    color: 'var(--text-ghost)',
    fontSize: 14,
    cursor: 'pointer',
    padding: '0 2px',
    lineHeight: 1,
  },

  // Body
  body: {
    flex: 1,
    display: 'flex',
    minHeight: 0,
    overflow: 'hidden',
  },

  // Sidebar
  sidebar: {
    width: 160,
    flexShrink: 0,
    borderRight: '1px solid var(--border-default)',
    background: 'var(--bg-surface)',
    overflowY: 'auto',
    overflowX: 'hidden',
    padding: '8px 0',
    display: 'flex',
    flexDirection: 'column',
    gap: 4,
  },
  sidebarSection: {
    display: 'flex',
    flexDirection: 'column',
  },
  sidebarLabel: {
    fontSize: 10,
    color: 'var(--text-ghost)',
    textTransform: 'uppercase',
    letterSpacing: '0.05em',
    padding: '6px 12px 4px',
    userSelect: 'none',
  },
  sidebarItem: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    padding: '5px 12px',
    background: 'none',
    border: 'none',
    color: 'var(--text-muted)',
    fontSize: 12,
    cursor: 'pointer',
    textAlign: 'left',
    width: '100%',
    borderRadius: 0,
  },
  sidebarItemActive: {
    background: 'var(--bg-selected)',
    color: 'var(--text-primary)',
  },
  sidebarIcon: {
    width: 16,
    textAlign: 'center',
    fontSize: 13,
    flexShrink: 0,
    opacity: 0.7,
  },

  // Main content area
  mainArea: {
    flex: 1,
    display: 'flex',
    flexDirection: 'column',
    minWidth: 0,
    overflow: 'hidden',
  },
  colHeader: {
    display: 'flex',
    alignItems: 'center',
    padding: '4px 8px',
    borderBottom: '1px solid var(--border-default)',
    fontSize: 10,
    color: 'var(--text-ghost)',
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    flexShrink: 0,
    userSelect: 'none',
  },
  colName: { flex: 1, display: 'flex', alignItems: 'center', gap: 6, minWidth: 0, overflow: 'hidden' },
  colSize: { width: 64, flexShrink: 0, textAlign: 'right', color: 'var(--text-ghost)' },
  colMod: { width: 120, flexShrink: 0, textAlign: 'right', color: 'var(--text-dim)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' },
  listScroll: {
    flex: 1,
    overflowY: 'auto',
    overflowX: 'hidden',
  },
  row: {
    display: 'flex',
    alignItems: 'center',
    padding: '4px 8px',
    cursor: 'pointer',
    borderBottom: '1px solid var(--border-subtle)',
  },
  rowSelected: {
    background: 'var(--bg-selected)',
    borderColor: 'var(--bg-selected-border)',
  },
  icon: {
    width: 16,
    textAlign: 'center',
    fontSize: 12,
    flexShrink: 0,
    color: 'var(--text-ghost)',
  },
  fileName: {
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  linkTarget: {
    color: 'var(--text-dim)',
    fontSize: 10,
    marginLeft: 4,
    flexShrink: 0,
  },
  matchScore: {
    background: '#2d1b69',
    color: '#a78bfa',
    fontSize: 9,
    padding: '1px 4px',
    borderRadius: 3,
    marginLeft: 6,
    flexShrink: 0,
  },
  empty: {
    padding: 20,
    textAlign: 'center',
    color: 'var(--text-invisible)',
    fontSize: 12,
  },

  // Preview pane
  previewPane: {
    width: 260,
    borderLeft: '1px solid var(--border-default)',
    display: 'flex',
    flexDirection: 'column',
    flexShrink: 0,
    overflow: 'hidden',
    background: 'var(--bg-base)',
  },
  previewHeader: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: '6px 10px',
    borderBottom: '1px solid var(--border-default)',
    flexShrink: 0,
  },
  previewTitle: {
    fontSize: 11,
    fontWeight: 500,
    color: 'var(--text-secondary)',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  previewClose: {
    background: 'none',
    border: 'none',
    color: 'var(--text-ghost)',
    fontSize: 14,
    cursor: 'pointer',
    padding: '0 2px',
    flexShrink: 0,
  },
  previewMeta: {
    padding: '8px 10px',
    borderBottom: '1px solid var(--border-subtle)',
    flexShrink: 0,
  },
  metaRow: {
    display: 'flex',
    justifyContent: 'space-between',
    fontSize: 11,
    color: 'var(--text-muted)',
    padding: '2px 0',
  },
  metaLabel: {
    color: 'var(--text-ghost)',
  },
  previewBody: {
    flex: 1,
    overflow: 'auto',
    minHeight: 0,
  },
  previewCode: {
    margin: 0,
    padding: 10,
    fontSize: 11,
    lineHeight: 1.5,
    color: 'var(--text-muted)',
    whiteSpace: 'pre-wrap',
    wordBreak: 'break-all',
    fontFamily: 'inherit',
  },

  // Status bar
  statusBar: {
    display: 'flex',
    justifyContent: 'space-between',
    padding: '3px 10px',
    background: 'var(--bg-surface)',
    borderTop: '1px solid var(--border-default)',
    fontSize: 10,
    color: 'var(--text-dim)',
    flexShrink: 0,
  },
}
