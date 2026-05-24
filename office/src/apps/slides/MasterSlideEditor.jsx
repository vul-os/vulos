/**
 * MasterSlideEditor.jsx — master-slide editor overlay.
 *
 * Allows editing:
 *  - 3 masters: Title, Content, Section.
 *  - Title placeholder Y-position and alignment.
 *  - Body placeholder Y-position and alignment.
 *  - Footer text.
 *  - Show/hide logo placeholder.
 *
 * Changes are saved to slidesData.masters[] and propagate to all slides
 * that have master === master.id via the preview rendering.
 */

import { useState } from 'react'
import { X, Layout } from 'lucide-react'
import { MASTER_LAYOUTS } from './themes.js'

const ALIGN_OPTIONS = ['left', 'center', 'right']

function MasterCard({ master, active, onClick }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={[
        'w-full text-left px-3 py-2.5 rounded-lg border-2 transition-all duration-fast',
        active
          ? 'border-accent bg-accent-tint text-accent'
          : 'border-line bg-bg text-ink hover:border-line-strong',
      ].join(' ')}
    >
      <div className="text-xs font-semibold">{master.label}</div>
      <div className="text-2xs text-ink-faint mt-0.5">
        Title @ {master.titleY} · Body @ {master.bodyY}
      </div>
    </button>
  )
}

export default function MasterSlideEditor({ masters, onSave, onClose }) {
  // Merge defaults with any saved overrides.
  const initialMasters = MASTER_LAYOUTS.map((def) => {
    const saved = (masters || []).find((m) => m.id === def.id)
    return saved ? { ...def, ...saved } : { ...def }
  })

  const [localMasters, setLocalMasters] = useState(initialMasters)
  const [activeMasterId, setActiveMasterId] = useState(initialMasters[0].id)

  const active = localMasters.find((m) => m.id === activeMasterId)

  const updateActive = (key, value) => {
    setLocalMasters((prev) =>
      prev.map((m) => m.id === activeMasterId ? { ...m, [key]: value } : m)
    )
  }

  const handleSave = () => {
    onSave(localMasters)
    onClose()
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 animate-fade-in"
      role="dialog"
      aria-modal="true"
      aria-label="Master slide editor"
      onClick={(e) => { if (e.target === e.currentTarget) onClose() }}
    >
      <div className="bg-paper border border-line rounded-xl shadow-e3 w-[720px] max-h-[90vh] flex flex-col overflow-hidden">
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-3 border-b border-line">
          <div className="flex items-center gap-2">
            <Layout size={15} className="text-accent" />
            <span className="font-semibold text-ink text-sm">Master Slide Editor</span>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="text-ink-faint hover:text-ink p-1 rounded-md transition-colors"
            aria-label="Close"
          >
            <X size={15} />
          </button>
        </div>

        <div className="flex flex-1 overflow-hidden">
          {/* Master selector */}
          <aside className="w-52 border-r border-line p-3 space-y-2 flex-shrink-0">
            <p className="text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow mb-2">
              Masters
            </p>
            {localMasters.map((m) => (
              <MasterCard
                key={m.id}
                master={m}
                active={activeMasterId === m.id}
                onClick={() => setActiveMasterId(m.id)}
              />
            ))}
            <p className="text-2xs text-ink-faint mt-3 leading-relaxed">
              Each slide is assigned one of these masters. Changes here propagate
              to all slides using that master.
            </p>
          </aside>

          {/* Editor + preview */}
          <div className="flex-1 p-5 overflow-y-auto space-y-5">
            {active && (
              <>
                <div className="grid grid-cols-2 gap-4">
                  {/* Title placeholder */}
                  <div className="space-y-2">
                    <h3 className="text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow">
                      Title Placeholder
                    </h3>
                    <div>
                      <label className="block text-2xs text-ink-muted mb-1">Vertical position</label>
                      <input
                        type="text"
                        value={active.titleY}
                        onChange={(e) => updateActive('titleY', e.target.value)}
                        className="w-full bg-bg text-ink text-xs rounded-sm px-2 h-7 border border-line focus:border-accent outline-none"
                        placeholder="e.g. 38%"
                      />
                    </div>
                    <div>
                      <label className="block text-2xs text-ink-muted mb-1">Text alignment</label>
                      <div className="flex gap-1">
                        {ALIGN_OPTIONS.map((a) => (
                          <button
                            key={a}
                            type="button"
                            aria-pressed={active.titleAlign === a}
                            onClick={() => updateActive('titleAlign', a)}
                            className={[
                              'flex-1 py-1 text-2xs rounded-sm border capitalize transition-colors',
                              active.titleAlign === a
                                ? 'border-accent bg-accent-tint text-accent'
                                : 'border-line text-ink-muted hover:border-line-strong',
                            ].join(' ')}
                          >
                            {a}
                          </button>
                        ))}
                      </div>
                    </div>
                  </div>

                  {/* Body placeholder */}
                  <div className="space-y-2">
                    <h3 className="text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow">
                      Body Placeholder
                    </h3>
                    <div>
                      <label className="block text-2xs text-ink-muted mb-1">Vertical position</label>
                      <input
                        type="text"
                        value={active.bodyY}
                        onChange={(e) => updateActive('bodyY', e.target.value)}
                        className="w-full bg-bg text-ink text-xs rounded-sm px-2 h-7 border border-line focus:border-accent outline-none"
                        placeholder="e.g. 62%"
                      />
                    </div>
                    <div>
                      <label className="block text-2xs text-ink-muted mb-1">Text alignment</label>
                      <div className="flex gap-1">
                        {ALIGN_OPTIONS.map((a) => (
                          <button
                            key={a}
                            type="button"
                            aria-pressed={active.bodyAlign === a}
                            onClick={() => updateActive('bodyAlign', a)}
                            className={[
                              'flex-1 py-1 text-2xs rounded-sm border capitalize transition-colors',
                              active.bodyAlign === a
                                ? 'border-accent bg-accent-tint text-accent'
                                : 'border-line text-ink-muted hover:border-line-strong',
                            ].join(' ')}
                          >
                            {a}
                          </button>
                        ))}
                      </div>
                    </div>
                  </div>
                </div>

                {/* Footer + logo */}
                <div className="grid grid-cols-2 gap-4">
                  <div>
                    <label className="block text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow mb-1">
                      Footer Text
                    </label>
                    <input
                      type="text"
                      value={active.footerText}
                      onChange={(e) => updateActive('footerText', e.target.value)}
                      className="w-full bg-bg text-ink text-xs rounded-sm px-2 h-7 border border-line focus:border-accent outline-none"
                      placeholder="Company name, date, etc."
                    />
                  </div>
                  <div className="flex items-end">
                    <label className="flex items-center gap-2 cursor-pointer select-none">
                      <input
                        type="checkbox"
                        checked={active.showLogo}
                        onChange={(e) => updateActive('showLogo', e.target.checked)}
                        className="rounded"
                      />
                      <span className="text-xs text-ink">Show logo placeholder</span>
                    </label>
                  </div>
                </div>

                {/* Mini preview */}
                <div>
                  <p className="text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow mb-2">
                    Layout Preview
                  </p>
                  <div
                    className="relative rounded-lg border border-line bg-zinc-900 overflow-hidden"
                    style={{ aspectRatio: '16/9' }}
                  >
                    {/* title */}
                    <div
                      className="absolute left-4 right-4 text-sm font-bold text-white"
                      style={{ top: active.titleY, textAlign: active.titleAlign }}
                    >
                      Slide Title
                    </div>
                    {/* body */}
                    <div
                      className="absolute left-4 right-4 text-xs text-white/60"
                      style={{ top: active.bodyY, textAlign: active.bodyAlign }}
                    >
                      Body content placeholder
                    </div>
                    {/* footer */}
                    {active.footerText && (
                      <div className="absolute bottom-2 left-4 right-4 text-2xs text-white/40 text-center">
                        {active.footerText}
                      </div>
                    )}
                    {/* logo */}
                    {active.showLogo && (
                      <div className="absolute top-2 right-3 w-8 h-4 rounded-sm bg-white/20 flex items-center justify-center">
                        <span className="text-[8px] text-white/50 font-bold">LOGO</span>
                      </div>
                    )}
                    {/* safe-area lines */}
                    <div className="absolute inset-2 border border-white/10 rounded pointer-events-none" />
                  </div>
                </div>
              </>
            )}
          </div>
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
            onClick={handleSave}
            className="text-xs bg-accent text-white px-4 py-1.5 rounded-md hover:bg-accent/90 transition-colors font-semibold"
          >
            Save masters
          </button>
        </div>
      </div>
    </div>
  )
}
