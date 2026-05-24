/**
 * ThemeGallery.jsx — modal for choosing / customising a slide theme.
 *
 * Features:
 *  • 15 preset theme tiles (font families, colour palette, dark/light bg).
 *  • Custom theme editor: pick heading/body fonts, primary/secondary/accent/bg.
 *  • Apply-to-all-slides via onApply callback.
 *  • Import theme from existing deck via JSON file upload (reads themeId +
 *    customTheme fields from deck metadata).
 */

import { useState, useRef } from 'react'
import { X, Upload, Check, Palette } from 'lucide-react'
import { PRESET_THEMES, getTheme } from './themes.js'

const FONT_OPTIONS = [
  '"Inter", sans-serif',
  '"Sora", sans-serif',
  '"DM Sans", sans-serif',
  '"Roboto", sans-serif',
  '"Open Sans", sans-serif',
  '"Helvetica Neue", Helvetica, sans-serif',
  '"Fira Code", monospace',
  '"Space Grotesk", sans-serif',
  '"Playfair Display", serif',
  '"Merriweather", serif',
  '"Palatino Linotype", serif',
  '"Cambria", "Times New Roman", serif',
  '"Georgia", serif',
  'Impact, sans-serif',
]

function ThemeTile({ theme, active, onSelect }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={() => onSelect(theme.id)}
      className={[
        'relative rounded-lg border-2 overflow-hidden transition-all duration-fast',
        'focus-visible:outline-none focus-visible:shadow-focus',
        active ? 'border-accent shadow-e2' : 'border-line hover:border-line-strong',
      ].join(' ')}
      style={{ background: theme.background }}
    >
      {/* mini slide preview */}
      <div className="p-2.5 h-24 flex flex-col justify-between">
        <div
          className="text-xs font-bold truncate"
          style={{ color: theme.text, fontFamily: theme.headingFont }}
        >
          {theme.label}
        </div>
        <div className="space-y-1">
          <div className="h-1 rounded-full" style={{ background: theme.primary, width: '70%' }} />
          <div className="h-1 rounded-full" style={{ background: theme.textMuted, width: '50%' }} />
          <div className="h-1 rounded-full" style={{ background: theme.textMuted, width: '40%' }} />
        </div>
        <div
          className="flex gap-1"
        >
          {[theme.primary, theme.secondary, theme.accent].map((c, i) => (
            <span
              key={i}
              className="inline-block rounded-full"
              style={{ background: c, width: 8, height: 8 }}
            />
          ))}
        </div>
      </div>
      {active && (
        <span className="absolute top-1.5 right-1.5 bg-accent text-white rounded-full p-0.5">
          <Check size={9} strokeWidth={3} />
        </span>
      )}
    </button>
  )
}

export default function ThemeGallery({ currentThemeId, customTheme, onApply, onClose }) {
  const [selected, setSelected] = useState(currentThemeId || PRESET_THEMES[0].id)
  const [custom, setCustom] = useState(customTheme || null)
  const [tab, setTab] = useState('gallery') // 'gallery' | 'custom'
  const importRef = useRef(null)

  // derive preview theme
  const previewTheme = tab === 'custom' && custom
    ? { ...getTheme(selected), ...custom }
    : getTheme(selected)

  const handleImport = (e) => {
    const file = e.target.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = (ev) => {
      try {
        const data = JSON.parse(ev.target.result)
        if (data.themeId) setSelected(data.themeId)
        if (data.customTheme && typeof data.customTheme === 'object') {
          setCustom(data.customTheme)
          setTab('custom')
        }
      } catch { /* ignore invalid JSON */ }
    }
    reader.readAsText(file)
    e.target.value = ''
  }

  const handleApply = () => {
    onApply({
      themeId: selected,
      customTheme: tab === 'custom' ? custom : null,
    })
    onClose()
  }

  const updateCustom = (key, value) => {
    setCustom((prev) => ({ ...(prev || {}), [key]: value }))
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 animate-fade-in"
      role="dialog"
      aria-modal="true"
      aria-label="Theme gallery"
      onClick={(e) => { if (e.target === e.currentTarget) onClose() }}
    >
      <div className="bg-paper border border-line rounded-xl shadow-e3 w-[660px] max-h-[90vh] flex flex-col overflow-hidden">
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-3 border-b border-line">
          <div className="flex items-center gap-2">
            <Palette size={15} className="text-accent" />
            <span className="font-semibold text-ink text-sm">Theme Gallery</span>
          </div>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => importRef.current?.click()}
              className="inline-flex items-center gap-1.5 text-xs text-ink-muted hover:text-ink px-2 py-1 rounded-md border border-line hover:border-line-strong transition-colors"
            >
              <Upload size={12} /> Import from deck
            </button>
            <input ref={importRef} type="file" accept=".json" className="hidden" onChange={handleImport} />
            <button
              type="button"
              onClick={onClose}
              className="text-ink-faint hover:text-ink p-1 rounded-md transition-colors"
              aria-label="Close"
            >
              <X size={15} />
            </button>
          </div>
        </div>

        {/* Tabs */}
        <div className="flex border-b border-line">
          {[['gallery', 'Presets'], ['custom', 'Custom']].map(([key, label]) => (
            <button
              key={key}
              type="button"
              onClick={() => setTab(key)}
              className={[
                'px-4 py-2.5 text-xs font-semibold transition-colors border-b-2 -mb-px',
                tab === key
                  ? 'border-accent text-accent'
                  : 'border-transparent text-ink-muted hover:text-ink',
              ].join(' ')}
            >
              {label}
            </button>
          ))}
        </div>

        <div className="flex-1 overflow-y-auto p-4">
          {tab === 'gallery' && (
            <div className="grid grid-cols-5 gap-2.5">
              {PRESET_THEMES.map((theme) => (
                <ThemeTile
                  key={theme.id}
                  theme={theme}
                  active={selected === theme.id}
                  onSelect={setSelected}
                />
              ))}
            </div>
          )}

          {tab === 'custom' && (
            <div className="space-y-4">
              <p className="text-xs text-ink-muted">
                Start from the selected preset, then override any values.
              </p>
              {/* Base preset */}
              <div>
                <label className="block text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow mb-1">
                  Base Preset
                </label>
                <select
                  value={selected}
                  onChange={(e) => setSelected(e.target.value)}
                  className="w-full bg-bg text-ink text-xs rounded-sm px-2 h-8 border border-line"
                >
                  {PRESET_THEMES.map((t) => (
                    <option key={t.id} value={t.id}>{t.label}</option>
                  ))}
                </select>
              </div>

              {/* Fonts */}
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow mb-1">
                    Heading Font
                  </label>
                  <select
                    value={custom?.headingFont || previewTheme.headingFont}
                    onChange={(e) => updateCustom('headingFont', e.target.value)}
                    className="w-full bg-bg text-ink text-xs rounded-sm px-2 h-8 border border-line"
                  >
                    {FONT_OPTIONS.map((f) => <option key={f} value={f}>{f.split('"')[1] || f}</option>)}
                  </select>
                </div>
                <div>
                  <label className="block text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow mb-1">
                    Body Font
                  </label>
                  <select
                    value={custom?.bodyFont || previewTheme.bodyFont}
                    onChange={(e) => updateCustom('bodyFont', e.target.value)}
                    className="w-full bg-bg text-ink text-xs rounded-sm px-2 h-8 border border-line"
                  >
                    {FONT_OPTIONS.map((f) => <option key={f} value={f}>{f.split('"')[1] || f}</option>)}
                  </select>
                </div>
              </div>

              {/* Colours */}
              <div className="grid grid-cols-4 gap-3">
                {[
                  ['primary', 'Primary'],
                  ['secondary', 'Secondary'],
                  ['accent', 'Accent'],
                  ['background', 'Background'],
                  ['text', 'Text'],
                  ['textMuted', 'Text Muted'],
                ].map(([key, lbl]) => (
                  <div key={key}>
                    <label className="block text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow mb-1">
                      {lbl}
                    </label>
                    <div className="flex items-center gap-1.5">
                      <input
                        type="color"
                        value={custom?.[key] || previewTheme[key] || '#000000'}
                        onChange={(e) => updateCustom(key, e.target.value)}
                        className="w-8 h-8 rounded border border-line cursor-pointer bg-transparent"
                        style={{ padding: 2 }}
                      />
                      <span className="text-xs text-ink-muted font-mono">
                        {(custom?.[key] || previewTheme[key] || '').toUpperCase()}
                      </span>
                    </div>
                  </div>
                ))}
              </div>

              {/* Live preview strip */}
              <div
                className="rounded-lg p-5 border border-line"
                style={{ background: custom?.background || previewTheme.background }}
              >
                <p
                  className="text-xl font-bold"
                  style={{
                    color: custom?.text || previewTheme.text,
                    fontFamily: custom?.headingFont || previewTheme.headingFont,
                  }}
                >
                  Slide heading preview
                </p>
                <p
                  className="text-sm mt-1"
                  style={{
                    color: custom?.textMuted || previewTheme.textMuted,
                    fontFamily: custom?.bodyFont || previewTheme.bodyFont,
                  }}
                >
                  Body copy goes here — lorem ipsum dolor sit amet.
                </p>
                <div className="flex gap-2 mt-3">
                  {['primary', 'secondary', 'accent'].map((k) => (
                    <span
                      key={k}
                      className="inline-block rounded-md px-3 py-1 text-xs font-semibold text-white"
                      style={{ background: custom?.[k] || previewTheme[k] }}
                    >
                      {k}
                    </span>
                  ))}
                </div>
              </div>
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="flex items-center justify-end gap-2 px-5 py-3 border-t border-line bg-clay">
          <button
            type="button"
            onClick={onClose}
            className="text-xs text-ink-muted hover:text-ink px-3 py-1.5 rounded-md border border-line hover:border-line-strong transition-colors"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={handleApply}
            className="text-xs bg-accent text-white px-4 py-1.5 rounded-md hover:bg-accent/90 transition-colors font-semibold"
          >
            Apply to all slides
          </button>
        </div>
      </div>
    </div>
  )
}
