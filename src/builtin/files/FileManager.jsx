import { useState, useEffect, useCallback, useRef } from 'react'

/* ── SVG Icon Components ── */

const IconFolder = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path d="M2 4a2 2 0 012-2h3.17a2 2 0 011.66.9L9.76 4H16a2 2 0 012 2v8a2 2 0 01-2 2H4a2 2 0 01-2-2V4z" />
  </svg>
)

const IconFile = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M4 4a2 2 0 012-2h4.586A2 2 0 0112 2.586L15.414 6A2 2 0 0116 7.414V16a2 2 0 01-2 2H6a2 2 0 01-2-2V4z" clipRule="evenodd" />
  </svg>
)

const IconCode = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M12.316 3.051a1 1 0 01.633 1.265l-4 12a1 1 0 11-1.898-.632l4-12a1 1 0 011.265-.633zM5.707 6.293a1 1 0 010 1.414L3.414 10l2.293 2.293a1 1 0 11-1.414 1.414l-3-3a1 1 0 010-1.414l3-3a1 1 0 011.414 0zm8.586 0a1 1 0 011.414 0l3 3a1 1 0 010 1.414l-3 3a1 1 0 11-1.414-1.414L16.586 10l-2.293-2.293a1 1 0 010-1.414z" clipRule="evenodd" />
  </svg>
)

const IconImage = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M4 3a2 2 0 00-2 2v10a2 2 0 002 2h12a2 2 0 002-2V5a2 2 0 00-2-2H4zm12 12H4l4-8 3 6 2-4 3 6z" clipRule="evenodd" />
  </svg>
)

const IconMusic = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path d="M18 3a1 1 0 00-1.196-.98l-10 2A1 1 0 006 5v9.114A4.369 4.369 0 005 14c-1.657 0-3 .895-3 2s1.343 2 3 2 3-.895 3-2V7.82l8-1.6v5.894A4.37 4.37 0 0015 12c-1.657 0-3 .895-3 2s1.343 2 3 2 3-.895 3-2V3z" />
  </svg>
)

const IconVideo = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path d="M2 6a2 2 0 012-2h6a2 2 0 012 2v6a2 2 0 01-2 2H4a2 2 0 01-2-2V6zm12.553 1.106A1 1 0 0014 8v4a1 1 0 00.553.894l2 1A1 1 0 0018 13V7a1 1 0 00-1.447-.894l-2 1z" />
  </svg>
)

const IconArchive = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path d="M4 3a2 2 0 100 4h12a2 2 0 100-4H4z" />
    <path fillRule="evenodd" d="M3 8h14v7a2 2 0 01-2 2H5a2 2 0 01-2-2V8zm5 3a1 1 0 011-1h2a1 1 0 110 2H9a1 1 0 01-1-1z" clipRule="evenodd" />
  </svg>
)

const IconDocument = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M4 4a2 2 0 012-2h4.586A2 2 0 0112 2.586L15.414 6A2 2 0 0116 7.414V16a2 2 0 01-2 2H6a2 2 0 01-2-2V4zm2 6a1 1 0 011-1h6a1 1 0 110 2H7a1 1 0 01-1-1zm1 3a1 1 0 100 2h6a1 1 0 100-2H7z" clipRule="evenodd" />
  </svg>
)

const IconTerminal = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M2 5a2 2 0 012-2h12a2 2 0 012 2v10a2 2 0 01-2 2H4a2 2 0 01-2-2V5zm3.293 1.293a1 1 0 011.414 0l3 3a1 1 0 010 1.414l-3 3a1 1 0 01-1.414-1.414L7.586 10 5.293 7.707a1 1 0 010-1.414zM11 12a1 1 0 100 2h3a1 1 0 100-2h-3z" clipRule="evenodd" />
  </svg>
)

const IconConfig = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M11.49 3.17c-.38-1.56-2.6-1.56-2.98 0a1.532 1.532 0 01-2.286.948c-1.372-.836-2.942.734-2.106 2.106.54.886.061 2.042-.947 2.287-1.561.379-1.561 2.6 0 2.978a1.532 1.532 0 01.947 2.287c-.836 1.372.734 2.942 2.106 2.106a1.532 1.532 0 012.287.947c.379 1.561 2.6 1.561 2.978 0a1.533 1.533 0 012.287-.947c1.372.836 2.942-.734 2.106-2.106a1.533 1.533 0 01.947-2.287c1.561-.379 1.561-2.6 0-2.978a1.532 1.532 0 01-.947-2.287c.836-1.372-.734-2.942-2.106-2.106a1.532 1.532 0 01-2.287-.947zM10 13a3 3 0 100-6 3 3 0 000 6z" clipRule="evenodd" />
  </svg>
)

const IconLock = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M5 9V7a5 5 0 0110 0v2a2 2 0 012 2v5a2 2 0 01-2 2H5a2 2 0 01-2-2v-5a2 2 0 012-2zm8-2v2H7V7a3 3 0 016 0z" clipRule="evenodd" />
  </svg>
)

const IconLink = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M12.586 4.586a2 2 0 112.828 2.828l-3 3a2 2 0 01-2.828 0 1 1 0 00-1.414 1.414 4 4 0 005.656 0l3-3a4 4 0 00-5.656-5.656l-1.5 1.5a1 1 0 101.414 1.414l1.5-1.5zm-5 5a2 2 0 012.828 0 1 1 0 101.414-1.414 4 4 0 00-5.656 0l-3 3a4 4 0 105.656 5.656l1.5-1.5a1 1 0 10-1.414-1.414l-1.5 1.5a2 2 0 11-2.828-2.828l3-3z" clipRule="evenodd" />
  </svg>
)

const IconPdf = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M4 4a2 2 0 012-2h4.586A2 2 0 0112 2.586L15.414 6A2 2 0 0116 7.414V16a2 2 0 01-2 2H6a2 2 0 01-2-2V4z" clipRule="evenodd" />
    <path d="M7 10h1.5a1 1 0 010 2H7v2H6v-5h2.5a1 1 0 010 2H7z" fill="currentColor" opacity="0.5" />
  </svg>
)

/* ── Nav icons ── */

const IconBack = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M9.707 16.707a1 1 0 01-1.414 0l-6-6a1 1 0 010-1.414l6-6a1 1 0 011.414 1.414L5.414 9H17a1 1 0 110 2H5.414l4.293 4.293a1 1 0 010 1.414z" clipRule="evenodd" />
  </svg>
)

const IconForward = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M10.293 3.293a1 1 0 011.414 0l6 6a1 1 0 010 1.414l-6 6a1 1 0 01-1.414-1.414L14.586 11H3a1 1 0 110-2h11.586l-4.293-4.293a1 1 0 010-1.414z" clipRule="evenodd" />
  </svg>
)

const IconUp = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M3.293 9.707a1 1 0 010-1.414l6-6a1 1 0 011.414 0l6 6a1 1 0 01-1.414 1.414L11 5.414V17a1 1 0 11-2 0V5.414L4.707 9.707a1 1 0 01-1.414 0z" clipRule="evenodd" />
  </svg>
)

const IconRefresh = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M4 2a1 1 0 011 1v2.101a7.002 7.002 0 0111.601 2.566 1 1 0 11-1.885.666A5.002 5.002 0 005.999 7H9a1 1 0 010 2H4a1 1 0 01-1-1V3a1 1 0 011-1zm.008 9.057a1 1 0 011.276.61A5.002 5.002 0 0014.001 13H11a1 1 0 110-2h5a1 1 0 011 1v5a1 1 0 11-2 0v-2.101a7.002 7.002 0 01-11.601-2.566 1 1 0 01.61-1.276z" clipRule="evenodd" />
  </svg>
)

const IconSearch = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="14" height="14">
    <path fillRule="evenodd" d="M8 4a4 4 0 100 8 4 4 0 000-8zM2 8a6 6 0 1110.89 3.476l4.817 4.817a1 1 0 01-1.414 1.414l-4.816-4.816A6 6 0 012 8z" clipRule="evenodd" />
  </svg>
)

const IconClose = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="14" height="14">
    <path fillRule="evenodd" d="M4.293 4.293a1 1 0 011.414 0L10 8.586l4.293-4.293a1 1 0 111.414 1.414L11.414 10l4.293 4.293a1 1 0 01-1.414 1.414L10 11.414l-4.293 4.293a1 1 0 01-1.414-1.414L8.586 10 4.293 5.707a1 1 0 010-1.414z" clipRule="evenodd" />
  </svg>
)

/* ── Sidebar icons ── */

const IconHome = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path d="M10.707 2.293a1 1 0 00-1.414 0l-7 7a1 1 0 001.414 1.414L4 10.414V17a1 1 0 001 1h2a1 1 0 001-1v-2a1 1 0 011-1h2a1 1 0 011 1v2a1 1 0 001 1h2a1 1 0 001-1v-6.586l.293.293a1 1 0 001.414-1.414l-7-7z" />
  </svg>
)

const IconDesktop = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M3 5a2 2 0 012-2h10a2 2 0 012 2v8a2 2 0 01-2 2h-2.22l.123.489.122.489.012.048H15a1 1 0 110 2H5a1 1 0 110-2h2.022l.012-.048.122-.489L7.28 15H5a2 2 0 01-2-2V5zm5.771 10l-.123.489-.122.489h2.948l-.122-.489L11.229 15H8.771zM5 5h10v8H5V5z" clipRule="evenodd" />
  </svg>
)

const IconDocuments = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M4 4a2 2 0 012-2h4.586A2 2 0 0112 2.586L15.414 6A2 2 0 0116 7.414V16a2 2 0 01-2 2H6a2 2 0 01-2-2V4zm2 6a1 1 0 011-1h6a1 1 0 110 2H7a1 1 0 01-1-1zm1 3a1 1 0 100 2h6a1 1 0 100-2H7z" clipRule="evenodd" />
  </svg>
)

const IconDownload = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M3 17a1 1 0 011-1h12a1 1 0 110 2H4a1 1 0 01-1-1zm3.293-7.707a1 1 0 011.414 0L9 10.586V3a1 1 0 112 0v7.586l1.293-1.293a1 1 0 111.414 1.414l-3 3a1 1 0 01-1.414 0l-3-3a1 1 0 010-1.414z" clipRule="evenodd" />
  </svg>
)

const IconPictures = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M4 3a2 2 0 00-2 2v10a2 2 0 002 2h12a2 2 0 002-2V5a2 2 0 00-2-2H4zm12 12H4l4-8 3 6 2-4 3 6z" clipRule="evenodd" />
  </svg>
)

const IconDisk = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zM7 9H5v2h2V9zm8 0h-2v2h2V9zM9 9h2v2H9V9z" clipRule="evenodd" />
  </svg>
)

const IconTemp = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="16" height="16">
    <path fillRule="evenodd" d="M10 2a1 1 0 011 1v6.586l1.707-1.707a1 1 0 111.414 1.414l-3.414 3.414a1 1 0 01-1.414 0L5.879 9.293a1 1 0 011.414-1.414L9 9.586V3a1 1 0 011-1zM4 14a1 1 0 100 2h12a1 1 0 100-2H4z" clipRule="evenodd" />
  </svg>
)

const IconChevronRight = ({ className = '' }) => (
  <svg className={className} viewBox="0 0 20 20" fill="currentColor" width="12" height="12">
    <path fillRule="evenodd" d="M7.293 14.707a1 1 0 010-1.414L10.586 10 7.293 6.707a1 1 0 011.414-1.414l4 4a1 1 0 010 1.414l-4 4a1 1 0 01-1.414 0z" clipRule="evenodd" />
  </svg>
)

/* ── Extension -> icon mapping ── */

const CODE_EXTS = new Set(['js', 'jsx', 'ts', 'tsx', 'mjs', 'py', 'go', 'rs', 'c', 'cpp', 'h', 'java'])
const IMAGE_EXTS = new Set(['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'ico'])
const VIDEO_EXTS = new Set(['mp4', 'mov', 'avi', 'mkv', 'webm'])
const AUDIO_EXTS = new Set(['mp3', 'wav', 'flac', 'ogg'])
const ARCHIVE_EXTS = new Set(['zip', 'tar', 'gz', 'xz', 'bz2', '7z'])
const DOC_EXTS = new Set(['md', 'txt', 'log', 'csv'])
const CONFIG_EXTS = new Set(['json', 'yaml', 'yml', 'toml', 'xml', 'ini'])
const SHELL_EXTS = new Set(['sh', 'bash', 'zsh', 'fish'])
const PDF_EXTS = new Set(['pdf', 'doc', 'docx'])
const LOCK_EXTS = new Set(['lock', 'env'])

function FileIcon({ name, isDir, isLink, className = '' }) {
  if (isDir) return <IconFolder className={`text-blue-400 ${className}`} />
  if (isLink) return <IconLink className={`text-cyan-400 ${className}`} />
  const ext = name.split('.').pop().toLowerCase()
  if (CODE_EXTS.has(ext)) return <IconCode className={`text-emerald-400 ${className}`} />
  if (IMAGE_EXTS.has(ext)) return <IconImage className={`text-pink-400 ${className}`} />
  if (VIDEO_EXTS.has(ext)) return <IconVideo className={`text-purple-400 ${className}`} />
  if (AUDIO_EXTS.has(ext)) return <IconMusic className={`text-amber-400 ${className}`} />
  if (ARCHIVE_EXTS.has(ext)) return <IconArchive className={`text-orange-400 ${className}`} />
  if (DOC_EXTS.has(ext)) return <IconDocument className={`text-neutral-400 ${className}`} />
  if (CONFIG_EXTS.has(ext)) return <IconConfig className={`text-yellow-500 ${className}`} />
  if (SHELL_EXTS.has(ext)) return <IconTerminal className={`text-green-400 ${className}`} />
  if (PDF_EXTS.has(ext)) return <IconPdf className={`text-red-400 ${className}`} />
  if (LOCK_EXTS.has(ext)) return <IconLock className={`text-neutral-500 ${className}`} />
  return <IconFile className={`text-neutral-500 ${className}`} />
}

/* ── Sidebar places with icon components ── */

const SIDEBAR_PLACES = [
  { label: 'Home', path: '~', Icon: IconHome },
  { label: 'Desktop', path: '~/Desktop', Icon: IconDesktop },
  { label: 'Documents', path: '~/Documents', Icon: IconDocuments },
  { label: 'Downloads', path: '~/Downloads', Icon: IconDownload },
  { label: 'Pictures', path: '~/Pictures', Icon: IconPictures },
  { label: 'Music', path: '~/Music', Icon: IconMusic },
  { label: 'Videos', path: '~/Videos', Icon: IconVideo },
]

const SIDEBAR_SYSTEM = [
  { label: 'Root', path: '/', Icon: IconDisk },
  { label: 'Tmp', path: '/tmp', Icon: IconTemp },
]

/* ── Helpers ── */

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

/* ── Main Component ── */

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
    <div className="flex flex-col h-full bg-neutral-950 text-neutral-300 text-xs overflow-hidden select-none">
      {/* Toolbar */}
      <div className="flex items-center gap-1.5 px-2 py-1.5 bg-neutral-900 border-b border-neutral-800/60 shrink-0">
        {/* Nav buttons */}
        <div className="flex gap-0.5">
          <button
            className="w-7 h-7 flex items-center justify-center rounded-lg text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200 disabled:opacity-30 disabled:hover:bg-transparent disabled:hover:text-neutral-400 transition-colors"
            onClick={goBack}
            disabled={histIdx <= 0}
            title="Back"
          >
            <IconBack />
          </button>
          <button
            className="w-7 h-7 flex items-center justify-center rounded-lg text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200 disabled:opacity-30 disabled:hover:bg-transparent disabled:hover:text-neutral-400 transition-colors"
            onClick={goForward}
            disabled={histIdx >= history.length - 1}
            title="Forward"
          >
            <IconForward />
          </button>
          <button
            className="w-7 h-7 flex items-center justify-center rounded-lg text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200 disabled:opacity-30 disabled:hover:bg-transparent disabled:hover:text-neutral-400 transition-colors"
            onClick={goUp}
            disabled={cwd === '/'}
            title="Up"
          >
            <IconUp />
          </button>
        </div>

        {/* Breadcrumb / path input */}
        <div
          className="flex-1 min-w-0 bg-neutral-950/80 border border-neutral-800/50 rounded-lg px-2.5 py-1.5 cursor-text hover:border-neutral-700/60 transition-colors"
          onClick={() => setEditingPath(true)}
        >
          {editingPath ? (
            <form onSubmit={handlePathSubmit} className="contents">
              <input
                autoFocus
                value={pathInput}
                onChange={e => setPathInput(e.target.value)}
                onBlur={() => setEditingPath(false)}
                className="w-full bg-transparent border-none outline-none text-neutral-200 text-xs font-mono"
              />
            </form>
          ) : (
            <div className="flex items-center overflow-hidden whitespace-nowrap gap-0.5">
              {breadcrumbs.map((b, i) => (
                <span key={i} className="inline-flex items-center">
                  {i > 0 && <IconChevronRight className="text-neutral-700 mx-0.5 shrink-0" />}
                  <span
                    className="text-neutral-400 hover:text-neutral-200 hover:bg-neutral-800/50 px-1.5 py-0.5 rounded cursor-pointer transition-colors"
                    onClick={(e) => { e.stopPropagation(); loadDir(b.path) }}
                  >
                    {b.label}
                  </span>
                </span>
              ))}
            </div>
          )}
        </div>

        {/* Search */}
        <div className="flex items-center gap-1.5 bg-neutral-950/80 border border-neutral-800/50 rounded-lg px-2.5 py-1.5 w-44 shrink-0 focus-within:border-blue-500/40 transition-colors">
          <IconSearch className="text-neutral-600 shrink-0" />
          <input
            ref={searchRef}
            value={query}
            onChange={e => setQuery(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') doSearch(); if (e.key === 'Escape') { setQuery(''); setSearchResults(null) } }}
            placeholder="Search..."
            className="flex-1 min-w-0 bg-transparent border-none outline-none text-neutral-300 text-xs placeholder:text-neutral-600"
          />
          {query && (
            <button
              className="text-neutral-600 hover:text-neutral-300 transition-colors"
              onClick={() => { setQuery(''); setSearchResults(null) }}
            >
              <IconClose />
            </button>
          )}
        </div>

        {/* Hidden files toggle */}
        <button
          className={`w-7 h-7 flex items-center justify-center rounded-lg text-[10px] font-bold transition-colors
            ${hidden ? 'bg-blue-500/20 text-blue-400' : 'text-neutral-600 hover:bg-neutral-800 hover:text-neutral-300'}`}
          onClick={() => { setHidden(!hidden); loadDir(cwd, false) }}
          title="Show hidden files"
        >
          .*
        </button>

        {/* Refresh */}
        <button
          className="w-7 h-7 flex items-center justify-center rounded-lg text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200 transition-colors"
          onClick={() => loadDir(cwd, false)}
          title="Refresh"
        >
          <IconRefresh />
        </button>
      </div>

      <div className="flex flex-1 min-h-0 overflow-hidden">
        {/* Sidebar */}
        <div className="w-44 shrink-0 border-r border-neutral-800/40 bg-neutral-900/50 overflow-y-auto overflow-x-hidden py-3 flex flex-col gap-1">
          {/* Places */}
          <div className="flex flex-col">
            <div className="text-[10px] font-semibold text-neutral-600 uppercase tracking-wider px-3 pb-1.5">
              Places
            </div>
            {SIDEBAR_PLACES.map(place => (
              <button
                key={place.path}
                className={`flex items-center gap-2.5 px-3 py-[5px] mx-1.5 rounded-md text-xs text-left w-auto transition-colors
                  ${isActive(place)
                    ? 'bg-neutral-800/80 text-neutral-100'
                    : 'text-neutral-500 hover:text-neutral-300 hover:bg-neutral-800/40'}`}
                onClick={() => loadDir(place.path)}
              >
                <place.Icon className={`shrink-0 ${isActive(place) ? 'text-blue-400' : 'text-neutral-600'}`} />
                <span>{place.label}</span>
              </button>
            ))}
          </div>

          <div className="my-1.5 mx-3 border-t border-neutral-800/40" />

          {/* System */}
          <div className="flex flex-col">
            <div className="text-[10px] font-semibold text-neutral-600 uppercase tracking-wider px-3 pb-1.5">
              System
            </div>
            {SIDEBAR_SYSTEM.map(place => (
              <button
                key={place.path}
                className={`flex items-center gap-2.5 px-3 py-[5px] mx-1.5 rounded-md text-xs text-left w-auto transition-colors
                  ${isActive(place)
                    ? 'bg-neutral-800/80 text-neutral-100'
                    : 'text-neutral-500 hover:text-neutral-300 hover:bg-neutral-800/40'}`}
                onClick={() => loadDir(place.path)}
              >
                <place.Icon className={`shrink-0 ${isActive(place) ? 'text-blue-400' : 'text-neutral-600'}`} />
                <span>{place.label}</span>
              </button>
            ))}
          </div>
        </div>

        {/* Main content area */}
        <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
          {/* Column headers */}
          <div className="flex items-center px-3 py-1.5 border-b border-neutral-800/40 text-[10px] text-neutral-600 uppercase tracking-wide shrink-0 select-none bg-neutral-950/50">
            <span className="flex-1 flex items-center gap-1.5 min-w-0 cursor-pointer hover:text-neutral-400 transition-colors" onClick={() => toggleSort('name')}>
              Name{sortArrow('name')}
            </span>
            <span className="w-16 shrink-0 text-right cursor-pointer hover:text-neutral-400 transition-colors" onClick={() => toggleSort('size')}>
              Size{sortArrow('size')}
            </span>
            <span className="w-32 shrink-0 text-right cursor-pointer hover:text-neutral-400 transition-colors" onClick={() => toggleSort('modified')}>
              Modified{sortArrow('modified')}
            </span>
          </div>

          {/* File rows */}
          <div className="flex-1 overflow-y-auto overflow-x-hidden">
            {loading && (
              <div className="py-12 text-center text-neutral-700 text-xs">Loading...</div>
            )}

            {searchResults ? (
              searchResults.items.length === 0 ? (
                <div className="py-12 text-center text-neutral-700 text-xs">No results</div>
              ) : searchResults.type === 'semantic' ? (
                searchResults.items.map((r, i) => {
                  const p = r.metadata?.abs_path || r.metadata?.path || ''
                  const name = p.split('/').pop()
                  return (
                    <div
                      key={i}
                      className={`flex items-center px-3 py-[5px] cursor-pointer border-b border-neutral-800/20 transition-colors
                        ${selected === `s${i}` ? 'bg-blue-500/10 border-blue-500/20' : 'hover:bg-neutral-800/30'}`}
                      onClick={() => { setSelected(`s${i}`); setPreview({ type: 'semantic', name, path: p, score: r.score, content: r.content }) }}
                      onDoubleClick={() => {
                        const dir = p.split('/').slice(0, -1).join('/')
                        if (dir) loadDir(dir)
                      }}
                    >
                      <span className="flex-1 flex items-center gap-2 min-w-0 overflow-hidden">
                        <IconCode className="text-purple-400 shrink-0" />
                        <span className="truncate">{name}</span>
                        <span className="bg-purple-500/20 text-purple-300 text-[9px] px-1.5 py-0.5 rounded-full ml-1 shrink-0">
                          {Math.round((r.score || 0) * 100)}%
                        </span>
                      </span>
                      <span className="w-16 shrink-0 text-right text-neutral-700">{'\u2014'}</span>
                      <span className="w-32 shrink-0 text-right text-neutral-700 truncate text-[10px]">
                        {p.split('/').slice(0, -1).join('/')}
                      </span>
                    </div>
                  )
                })
              ) : (
                searchResults.items.map((r, i) => (
                  <div
                    key={i}
                    className={`flex items-center px-3 py-[5px] cursor-pointer border-b border-neutral-800/20 transition-colors
                      ${selected === `f${i}` ? 'bg-blue-500/10 border-blue-500/20' : 'hover:bg-neutral-800/30'}`}
                    onClick={() => setSelected(`f${i}`)}
                    onDoubleClick={() => {
                      const dir = r.path.split('/').slice(0, -1).join('/')
                      if (dir) loadDir(dir)
                    }}
                  >
                    <span className="flex-1 flex items-center gap-2 min-w-0 overflow-hidden">
                      <FileIcon name={r.name} isDir={false} isLink={false} />
                      <span className="truncate">{r.name}</span>
                    </span>
                    <span className="w-16 shrink-0 text-right text-neutral-700">{'\u2014'}</span>
                    <span className="w-32 shrink-0 text-right text-neutral-700 truncate text-[10px]">
                      {r.path}
                    </span>
                  </div>
                ))
              )
            ) : (
              sorted.map((entry, i) => (
                <div
                  key={entry.name}
                  className={`flex items-center px-3 py-[5px] cursor-pointer border-b border-neutral-800/20 transition-colors
                    ${selected === i ? 'bg-blue-500/10 border-blue-500/20' : 'hover:bg-neutral-800/30'}`}
                  onClick={() => selectEntry(entry, i)}
                  onDoubleClick={() => entry.isDir && navigate(entry.name)}
                >
                  <span className="flex-1 flex items-center gap-2 min-w-0 overflow-hidden">
                    <FileIcon name={entry.name} isDir={entry.isDir} isLink={entry.isLink} className="shrink-0" />
                    <span className={`truncate ${entry.isDir ? 'font-medium text-neutral-200' : ''}`}>
                      {entry.name}
                    </span>
                    {entry.linkTarget && (
                      <span className="text-neutral-600 text-[10px] ml-1 shrink-0 flex items-center gap-0.5">
                        <IconChevronRight className="text-neutral-700" /> {entry.linkTarget}
                      </span>
                    )}
                  </span>
                  <span className="w-16 shrink-0 text-right text-neutral-600">
                    {entry.isDir ? '\u2014' : fmtSize(entry.size)}
                  </span>
                  <span className="w-32 shrink-0 text-right text-neutral-600 truncate">
                    {entry.modified}
                  </span>
                </div>
              ))
            )}

            {!loading && !searchResults && entries.length === 0 && (
              <div className="py-12 text-center text-neutral-700 text-xs">Empty directory</div>
            )}
          </div>
        </div>

        {/* Preview pane */}
        {preview && (
          <div className="w-64 border-l border-neutral-800/40 flex flex-col shrink-0 overflow-hidden bg-neutral-900/30">
            {/* Preview header */}
            <div className="flex items-center justify-between px-3 py-2.5 border-b border-neutral-800/40 shrink-0">
              <div className="flex items-center gap-2 min-w-0 overflow-hidden">
                <FileIcon
                  name={preview.name}
                  isDir={preview.type === 'dir'}
                  isLink={preview.entry?.isLink}
                />
                <span className="text-xs font-medium text-neutral-200 truncate">
                  {preview.name}
                </span>
              </div>
              <button
                className="shrink-0 w-6 h-6 flex items-center justify-center rounded-md text-neutral-600 hover:text-neutral-300 hover:bg-neutral-800/60 transition-colors"
                onClick={() => { setPreview(null); setSelected(null) }}
              >
                <IconClose />
              </button>
            </div>

            {/* Metadata */}
            {preview.entry && (
              <div className="px-3 py-2.5 border-b border-neutral-800/30 shrink-0 space-y-1">
                {preview.entry.perms && (
                  <div className="flex justify-between text-[11px]">
                    <span className="text-neutral-600">Permissions</span>
                    <span className="text-neutral-400 font-mono text-[10px]">{preview.entry.perms}</span>
                  </div>
                )}
                {preview.entry.owner && (
                  <div className="flex justify-between text-[11px]">
                    <span className="text-neutral-600">Owner</span>
                    <span className="text-neutral-400">{preview.entry.owner}:{preview.entry.group}</span>
                  </div>
                )}
                {preview.entry.size !== undefined && !preview.entry.isDir && (
                  <div className="flex justify-between text-[11px]">
                    <span className="text-neutral-600">Size</span>
                    <span className="text-neutral-400">{fmtSize(preview.entry.size)}</span>
                  </div>
                )}
                {preview.entry.modified && (
                  <div className="flex justify-between text-[11px]">
                    <span className="text-neutral-600">Modified</span>
                    <span className="text-neutral-400">{preview.entry.modified}</span>
                  </div>
                )}
                {preview.entry.linkTarget && (
                  <div className="flex justify-between text-[11px]">
                    <span className="text-neutral-600">Link</span>
                    <span className="text-neutral-400 truncate ml-2">{preview.entry.linkTarget}</span>
                  </div>
                )}
              </div>
            )}

            {preview.type === 'semantic' && (
              <div className="px-3 py-2.5 border-b border-neutral-800/30 shrink-0 space-y-1">
                <div className="flex justify-between text-[11px]">
                  <span className="text-neutral-600">Match</span>
                  <span className="text-purple-400">{Math.round((preview.score || 0) * 100)}%</span>
                </div>
                <div className="flex justify-between text-[11px]">
                  <span className="text-neutral-600">Path</span>
                  <span className="text-neutral-500 text-[10px] truncate ml-2">{preview.path}</span>
                </div>
              </div>
            )}

            {/* Preview body */}
            <div className="flex-1 overflow-auto min-h-0">
              {previewLoading ? (
                <div className="p-3 text-neutral-600 text-xs">Loading...</div>
              ) : preview.type === 'dir' ? (
                <div className="p-4 text-center">
                  <IconFolder className="text-blue-400/40 mx-auto mb-2" />
                  <div className="text-neutral-600 text-xs">Double-click to open directory</div>
                </div>
              ) : preview.type === 'image' ? (
                <div className="p-4 text-center">
                  <IconImage className="text-pink-400/40 mx-auto mb-2" />
                  <div className="text-neutral-600 text-xs">Image: {preview.name}</div>
                </div>
              ) : (
                <pre className="m-0 p-3 text-[11px] leading-relaxed text-neutral-500 whitespace-pre-wrap break-all font-mono">
                  {preview.content}
                </pre>
              )}
            </div>
          </div>
        )}
      </div>

      {/* Status bar */}
      <div className="flex justify-between items-center px-3 py-1 bg-neutral-900/50 border-t border-neutral-800/40 text-[10px] text-neutral-600 shrink-0">
        <span>{entries.length} items</span>
        <span className="text-neutral-700 truncate ml-4">{cwd}</span>
      </div>
    </div>
  )
}
