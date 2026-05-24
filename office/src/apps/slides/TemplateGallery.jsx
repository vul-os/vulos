/**
 * TemplateGallery.jsx — pre-built deck template picker.
 *
 * Shows 4 templates (Pitch, Project Plan, Lesson Plan, Quarterly Review).
 * Each template seeds the full deck with preset slides + theme.
 * Calls onApply(templateDecks) which replaces slidesData.
 */

import { useState } from 'react'
import { X, FileText, Check } from 'lucide-react'
import { DECK_TEMPLATES, getTheme } from './themes.js'

export default function TemplateGallery({ onApply, onClose }) {
  const [selected, setSelected] = useState(null)

  const handleApply = () => {
    if (!selected) return
    const tpl = DECK_TEMPLATES.find((t) => t.id === selected)
    if (!tpl) return
    const theme = getTheme(tpl.themeId)
    // Seed each slide with a fresh UUID so the deck is independent of the template.
    const slides = tpl.slides.map((s) => ({ ...s, id: crypto.randomUUID() }))
    onApply({
      themeId: tpl.themeId,
      theme: theme.revealTheme,
      transition: 'fade',
      customTheme: null,
      slides,
    })
    onClose()
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 animate-fade-in"
      role="dialog"
      aria-modal="true"
      aria-label="Template gallery"
      onClick={(e) => { if (e.target === e.currentTarget) onClose() }}
    >
      <div className="bg-paper border border-line rounded-xl shadow-e3 w-[560px] max-h-[90vh] flex flex-col overflow-hidden">
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-3 border-b border-line">
          <div className="flex items-center gap-2">
            <FileText size={15} className="text-accent" />
            <span className="font-semibold text-ink text-sm">New from Template</span>
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

        <div className="flex-1 overflow-y-auto p-4 grid grid-cols-2 gap-3">
          {DECK_TEMPLATES.map((tpl) => {
            const theme = getTheme(tpl.themeId)
            const isSelected = selected === tpl.id
            return (
              <button
                key={tpl.id}
                type="button"
                aria-pressed={isSelected}
                onClick={() => setSelected(tpl.id)}
                className={[
                  'relative text-left rounded-xl border-2 overflow-hidden transition-all',
                  'focus-visible:outline-none focus-visible:shadow-focus',
                  isSelected ? 'border-accent shadow-e2' : 'border-line hover:border-line-strong',
                ].join(' ')}
                style={{ background: theme.background }}
              >
                {/* mini slide thumbnails */}
                <div className="p-3 h-32 flex flex-col justify-between overflow-hidden">
                  <div
                    className="text-sm font-bold"
                    style={{ color: theme.text, fontFamily: theme.headingFont }}
                  >
                    {tpl.label}
                  </div>
                  <div className="space-y-1">
                    {tpl.slides.slice(0, 4).map((s, i) => (
                      <div
                        key={i}
                        className="h-1 rounded-full"
                        style={{
                          background: i === 0 ? theme.primary : theme.textMuted,
                          width: i === 0 ? '80%' : `${60 - i * 10}%`,
                          opacity: 1 - i * 0.15,
                        }}
                      />
                    ))}
                  </div>
                  <div
                    className="text-xs"
                    style={{ color: theme.textMuted, fontFamily: theme.bodyFont }}
                  >
                    {tpl.description}
                  </div>
                  <div
                    className="text-2xs"
                    style={{ color: theme.textMuted }}
                  >
                    {tpl.slides.length} slides · {tpl.label}
                  </div>
                </div>
                {isSelected && (
                  <span className="absolute top-2 right-2 bg-accent text-white rounded-full p-0.5">
                    <Check size={10} strokeWidth={3} />
                  </span>
                )}
              </button>
            )
          })}

          {/* Blank deck option */}
          <button
            type="button"
            aria-pressed={selected === '_blank'}
            onClick={() => setSelected('_blank')}
            className={[
              'relative text-left rounded-xl border-2 overflow-hidden transition-all',
              'flex items-center justify-center',
              'focus-visible:outline-none focus-visible:shadow-focus',
              selected === '_blank'
                ? 'border-accent shadow-e2 bg-accent-tint'
                : 'border-line border-dashed hover:border-line-strong bg-bg',
            ].join(' ')}
            style={{ minHeight: 128 }}
          >
            <div className="text-center">
              <div className="text-xl font-bold text-ink-faint">+</div>
              <div className="text-xs text-ink-muted mt-1">Blank deck</div>
            </div>
          </button>
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
            disabled={!selected}
            className={[
              'text-xs px-4 py-1.5 rounded-md font-semibold transition-colors',
              selected
                ? 'bg-accent text-white hover:bg-accent/90'
                : 'bg-line text-ink-faint cursor-not-allowed',
            ].join(' ')}
          >
            Create deck
          </button>
        </div>
      </div>
    </div>
  )
}
