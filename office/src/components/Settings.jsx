import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  ArrowLeft, Save, Shield, Database, HardDrive,
  Eye, EyeOff, CheckCircle2, AlertCircle, Loader2,
  Sun, Moon, Monitor,
} from 'lucide-react'
import { useAuthStore } from '../store/authStore'
import { Button, IconButton, Input, Card, Tabs, Tooltip } from './ui'
import { useTheme } from './ui/useTheme'

// ─── Row ─────────────────────────────────────────────────────────────────────
function Row({ label, hint, children }) {
  return (
    <div className="flex items-center justify-between gap-6 px-5 py-4">
      <div className="min-w-0">
        <p className="text-sm font-medium text-ink tracking-tightish">{label}</p>
        {hint && <p className="text-2xs text-ink-faint mt-0.5 leading-snug">{hint}</p>}
      </div>
      <div className="flex-shrink-0">{children}</div>
    </div>
  )
}

// ─── Toggle ──────────────────────────────────────────────────────────────────
function Toggle({ checked, onChange }) {
  return (
    <button
      role="switch"
      aria-checked={checked}
      onClick={() => onChange(!checked)}
      className={[
        'relative inline-flex h-5 w-9 items-center rounded-pill transition-colors duration-base ease-out',
        'focus-visible:outline-none focus-visible:shadow-focus',
        checked ? 'bg-accent' : 'bg-line-strong',
      ].join(' ')}
    >
      <span
        className={[
          'inline-block h-3.5 w-3.5 rounded-pill bg-paper shadow-e1 transition-transform duration-base ease-spring',
          checked ? 'translate-x-[18px]' : 'translate-x-[3px]',
        ].join(' ')}
      />
    </button>
  )
}

// ─── ThemePicker ─────────────────────────────────────────────────────────────
function ThemePicker() {
  const { theme, setTheme } = useTheme()

  const options = [
    { value: 'light',  Icon: Sun,     label: 'Light' },
    { value: 'dark',   Icon: Moon,    label: 'Dark'  },
    { value: 'system', Icon: Monitor, label: 'Auto'  },
  ]

  return (
    <div className="flex items-center gap-0.5 p-0.5 bg-bg-elev2 border border-line rounded-md">
      {options.map(({ value, Icon, label }) => {
        const active = theme === value
        return (
          <Tooltip key={value} label={label} side="bottom">
            <button
              onClick={() => setTheme(value)}
              className={[
                'inline-flex items-center gap-1.5 h-7 px-2.5 rounded-sm text-xs font-medium',
                'tracking-tightish transition-[background,color] duration-fast ease-out',
                active
                  ? 'bg-paper text-ink shadow-e1'
                  : 'text-ink-faint hover:text-ink-muted',
              ].join(' ')}
            >
              <Icon size={13} />
              {label}
            </button>
          </Tooltip>
        )
      })}
    </div>
  )
}

// ─── TABS: Preferences / Security / Storage ──────────────────────────────────
const TABS = [
  { value: 'preferences', label: 'Preferences' },
  { value: 'security',    label: 'Security'    },
  { value: 'storage',     label: 'Storage'     },
]

export default function Settings() {
  const navigate = useNavigate()
  const { status } = useAuthStore()
  const [tab, setTab] = useState('preferences')

  // Password change
  const [showPassword, setShowPassword] = useState(false)
  const [newPassword, setNewPassword] = useState('')
  const [saving, setSaving] = useState(false)
  const [toast, setToast] = useState(null)

  // App preferences stored in localStorage
  const [autosaveDelay, setAutosaveDelay] = useState(() =>
    parseInt(localStorage.getItem('vulos_autosave_delay') ?? '2000')
  )
  const [spellcheck, setSpellcheck] = useState(() =>
    localStorage.getItem('vulos_spellcheck') !== 'false'
  )
  const [defaultView, setDefaultView] = useState(() =>
    localStorage.getItem('vulos_default_view') ?? 'grid'
  )

  const showToast = (msg, ok = true) => {
    setToast({ msg, ok })
    setTimeout(() => setToast(null), 2800)
  }

  const savePrefs = () => {
    localStorage.setItem('vulos_autosave_delay', autosaveDelay)
    localStorage.setItem('vulos_spellcheck', spellcheck)
    localStorage.setItem('vulos_default_view', defaultView)
    showToast('Preferences saved')
  }

  const changePassword = async () => {
    if (!newPassword.trim()) return
    setSaving(true)
    try {
      const res = await fetch('/api/auth/password', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: newPassword }),
      })
      if (res.ok) {
        setNewPassword('')
        showToast('Password updated — restart the server to apply')
      } else {
        const { error } = await res.json().catch(() => ({}))
        showToast(error || 'Failed to update password', false)
      }
    } catch {
      showToast('Server unreachable', false)
    } finally {
      setSaving(false)
    }
  }

  // ── current account identity (for warm serif nameplate) ────────────────────
  let identity = { displayName: 'You', accountId: '' }
  try {
    const stored = localStorage.getItem('presence_identity')
    if (stored) {
      const p = JSON.parse(stored)
      if (p?.displayName) identity = p
    }
  } catch {}

  return (
    <div className="flex-1 overflow-auto bg-bg">
      {/* ── Topbar ── */}
      <div className="sticky top-0 z-10 flex items-center gap-3 px-5 h-11 bg-paper border-b border-line">
        <Tooltip label="Back" side="bottom">
          <IconButton size="sm" onClick={() => navigate(-1)}>
            <ArrowLeft size={15} />
          </IconButton>
        </Tooltip>
        <h1 className="text-sm font-semibold text-ink tracking-tightish flex-1">Settings</h1>
        {/* Theme toggle — lives at the top per spec */}
        <ThemePicker />
      </div>

      <div className="max-w-2xl mx-auto px-6 py-8 space-y-6">

        {/* ── Account nameplate ── warm serif for the name */}
        <div className="flex items-center gap-4 px-5 py-4 bg-paper border border-line rounded-lg">
          {/* Avatar initial */}
          <div className="w-10 h-10 rounded-full bg-accent-tint border border-accent flex items-center justify-center flex-shrink-0">
            <span className="text-sm font-semibold text-accent-press select-none">
              {identity.displayName.slice(0, 1).toUpperCase()}
            </span>
          </div>
          <div className="min-w-0">
            <p className="font-serif text-lg text-ink leading-tight">{identity.displayName}</p>
            {identity.accountId && (
              <p className="text-2xs text-ink-faint tracking-tightish mt-0.5">{identity.accountId}</p>
            )}
          </div>
        </div>

        {/* ── Tabs ── */}
        <Card>
          <Tabs value={tab} onChange={setTab} items={TABS} />

          {/* ── Preferences ── */}
          {tab === 'preferences' && (
            <div>
              <Row label="Autosave delay" hint="How long after typing before changes are saved automatically">
                <select
                  value={autosaveDelay}
                  onChange={e => setAutosaveDelay(Number(e.target.value))}
                  className={[
                    'h-8 text-sm text-ink bg-paper border border-line rounded-md px-3',
                    'transition-[border-color,box-shadow] duration-fast ease-out',
                    'focus:outline-none focus:border-accent focus:shadow-focus',
                    'tracking-tightish',
                  ].join(' ')}
                >
                  <option value={1000}>1 second</option>
                  <option value={2000}>2 seconds</option>
                  <option value={3000}>3 seconds</option>
                  <option value={5000}>5 seconds</option>
                </select>
              </Row>

              <div className="border-t border-line">
                <Row label="Spell check" hint="Enable browser spell checking in document editors">
                  <Toggle checked={spellcheck} onChange={setSpellcheck} />
                </Row>
              </div>

              <div className="border-t border-line">
                <Row label="Default file view" hint="Grid or list view on app home pages">
                  <div className="flex items-center gap-0.5 p-0.5 bg-bg-elev2 border border-line rounded-md">
                    {['grid', 'list'].map(v => (
                      <button
                        key={v}
                        onClick={() => setDefaultView(v)}
                        className={[
                          'h-7 px-3 rounded-sm text-xs font-medium capitalize tracking-tightish',
                          'transition-[background,color,box-shadow] duration-fast ease-out',
                          defaultView === v
                            ? 'bg-paper text-ink shadow-e1'
                            : 'text-ink-faint hover:text-ink-muted',
                        ].join(' ')}
                      >
                        {v}
                      </button>
                    ))}
                  </div>
                </Row>
              </div>

              <div className="border-t border-line px-5 py-3 flex justify-end bg-bg-elev2">
                <Button variant="primary" size="sm" onClick={savePrefs}>
                  <Save size={13} /> Save preferences
                </Button>
              </div>
            </div>
          )}

          {/* ── Security ── */}
          {tab === 'security' && (
            <div>
              {status?.enabled ? (
                <>
                  <Row
                    label="Login protection"
                    hint="Password authentication is currently enabled"
                  >
                    <span className="inline-flex items-center gap-1.5 text-2xs font-semibold text-success bg-success-bg border border-success px-2.5 py-1 rounded-pill tracking-tightish">
                      <CheckCircle2 size={11} /> Enabled
                    </span>
                  </Row>
                  <div className="border-t border-line">
                    <Row
                      label="Change password"
                      hint="Updates config.yaml — requires server restart to take effect"
                    >
                      <div className="flex items-center gap-2">
                        <Input
                          type={showPassword ? 'text' : 'password'}
                          value={newPassword}
                          onChange={e => setNewPassword(e.target.value)}
                          placeholder="New password"
                          size="sm"
                          trailing={
                            <button
                              type="button"
                              onClick={() => setShowPassword(v => !v)}
                              className="text-ink-faint hover:text-ink transition-colors"
                            >
                              {showPassword ? <EyeOff size={13} /> : <Eye size={13} />}
                            </button>
                          }
                          className="w-44"
                        />
                        <Button
                          variant="primary"
                          size="sm"
                          onClick={changePassword}
                          disabled={!newPassword.trim() || saving}
                        >
                          {saving ? <Loader2 size={13} className="animate-spin" /> : <Save size={13} />}
                          Update
                        </Button>
                      </div>
                    </Row>
                  </div>
                </>
              ) : (
                <Row
                  label="Login protection"
                  hint="Enable via config.yaml: set auth.enabled: true"
                >
                  <span className="inline-flex items-center gap-1.5 text-2xs font-semibold text-ink-faint bg-bg-elev2 border border-line px-2.5 py-1 rounded-pill tracking-tightish">
                    <AlertCircle size={11} /> Disabled
                  </span>
                </Row>
              )}
            </div>
          )}

          {/* ── Storage ── */}
          {tab === 'storage' && (
            <div>
              <Row label="Storage backend" hint="Configured in config.yaml">
                <code className="text-2xs font-mono bg-bg-elev2 text-ink-muted border border-line px-2.5 py-1 rounded-md">
                  local files
                </code>
              </Row>
              <div className="border-t border-line">
                <Row label="Data directory" hint="Where your files are saved on disk">
                  <code className="text-2xs font-mono bg-bg-elev2 text-ink-muted border border-line px-2.5 py-1 rounded-md">
                    ./data/
                  </code>
                </Row>
              </div>
            </div>
          )}
        </Card>

        {/* About */}
        <p className="text-center text-2xs text-ink-faint tracking-eyebrow uppercase pb-4">
          Vulos Office — open-source local-first office suite
        </p>
      </div>

      {/* ── Toast ── */}
      {toast && (
        <div
          className={[
            'fixed bottom-6 left-1/2 -translate-x-1/2 flex items-center gap-2',
            'px-4 py-2.5 rounded-lg shadow-e3 text-sm font-medium text-white',
            'animate-rise-in',
            toast.ok ? 'bg-ink' : 'bg-danger',
          ].join(' ')}
        >
          {toast.ok ? <CheckCircle2 size={14} /> : <AlertCircle size={14} />}
          {toast.msg}
        </div>
      )}
    </div>
  )
}
