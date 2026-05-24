/**
 * TransitionPanel.jsx — per-slide transition picker + per-element animation panel.
 *
 * Transitions map directly to reveal.js data-transition attributes.
 * Element animations store a list in slide.animations[].
 *
 * Format of slide.animations:
 *   [{ id, label, type, effect, order }]
 *   type: 'entrance' | 'exit' | 'emphasis'
 *   effect: 'fade-in' | 'fly-in' | 'bounce' | 'zoom-in' | 'custom'
 */

import { useState } from 'react'
import { GripVertical, Plus, Trash2 } from 'lucide-react'

export const SLIDE_TRANSITIONS = [
  { id: 'none',    label: 'None' },
  { id: 'fade',    label: 'Fade' },
  { id: 'slide',   label: 'Slide' },
  { id: 'convex',  label: 'Push' },
  { id: 'concave', label: 'Reveal' },
  { id: 'zoom',    label: 'Zoom' },
]

const ANIMATION_TYPES = ['entrance', 'exit', 'emphasis']
const ANIMATION_EFFECTS = ['fade-in', 'fly-in', 'bounce', 'zoom-in', 'spin', 'custom']

export default function TransitionPanel({ slide, onChange }) {
  const transition = slide.transition || 'none'
  const animations = slide.animations || []
  const [dragIdx, setDragIdx] = useState(null)
  const [overIdx, setOverIdx] = useState(null)

  const setTransition = (t) => onChange({ ...slide, transition: t })

  const addAnimation = () => {
    const anim = {
      id: crypto.randomUUID(),
      label: 'New animation',
      type: 'entrance',
      effect: 'fade-in',
      order: animations.length,
    }
    onChange({ ...slide, animations: [...animations, anim] })
  }

  const removeAnimation = (id) => {
    onChange({
      ...slide,
      animations: animations.filter((a) => a.id !== id).map((a, i) => ({ ...a, order: i })),
    })
  }

  const updateAnimation = (id, key, value) => {
    onChange({
      ...slide,
      animations: animations.map((a) => a.id === id ? { ...a, [key]: value } : a),
    })
  }

  // Drag-reorder
  const handleDragEnd = () => {
    if (dragIdx === null || overIdx === null || dragIdx === overIdx) {
      setDragIdx(null); setOverIdx(null); return
    }
    const next = [...animations]
    const [item] = next.splice(dragIdx, 1)
    next.splice(overIdx, 0, item)
    onChange({ ...slide, animations: next.map((a, i) => ({ ...a, order: i })) })
    setDragIdx(null); setOverIdx(null)
  }

  return (
    <div className="space-y-4">
      {/* Transition picker */}
      <div>
        <p className="text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow mb-2">
          Slide Transition
        </p>
        <div className="grid grid-cols-3 gap-1.5">
          {SLIDE_TRANSITIONS.map((t) => (
            <button
              key={t.id}
              type="button"
              aria-pressed={transition === t.id}
              onClick={() => setTransition(t.id)}
              className={[
                'py-1.5 text-xs rounded-md border transition-colors',
                transition === t.id
                  ? 'border-accent bg-accent-tint text-accent font-semibold'
                  : 'border-line text-ink-muted hover:border-line-strong',
              ].join(' ')}
            >
              {t.label}
            </button>
          ))}
        </div>
      </div>

      {/* Animations */}
      <div>
        <div className="flex items-center justify-between mb-2">
          <p className="text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow">
            Animations
          </p>
          <button
            type="button"
            onClick={addAnimation}
            className="inline-flex items-center gap-1 text-2xs text-accent hover:text-accent/80 transition-colors"
          >
            <Plus size={10} /> Add
          </button>
        </div>

        {animations.length === 0 && (
          <p className="text-2xs text-ink-faint italic">No animations. Click Add to create one.</p>
        )}

        <div className="space-y-1.5">
          {animations.map((anim, i) => (
            <div
              key={anim.id}
              draggable
              onDragStart={() => setDragIdx(i)}
              onDragOver={(e) => { e.preventDefault(); setOverIdx(i) }}
              onDragEnd={handleDragEnd}
              className={[
                'flex items-center gap-1.5 p-1.5 rounded-md border bg-bg',
                overIdx === i && dragIdx !== i ? 'border-accent' : 'border-line',
              ].join(' ')}
            >
              <GripVertical size={12} className="text-ink-faint flex-shrink-0 cursor-grab" />
              <span className="text-2xs text-ink-faint w-5 text-right flex-shrink-0">
                {i + 1}.
              </span>
              <input
                type="text"
                value={anim.label}
                onChange={(e) => updateAnimation(anim.id, 'label', e.target.value)}
                className="flex-1 min-w-0 bg-transparent text-xs text-ink outline-none border-b border-transparent hover:border-line focus:border-accent"
                placeholder="Element label"
              />
              <select
                value={anim.type}
                onChange={(e) => updateAnimation(anim.id, 'type', e.target.value)}
                className="text-2xs bg-bg text-ink border border-line rounded-sm px-1 h-6"
              >
                {ANIMATION_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
              </select>
              <select
                value={anim.effect}
                onChange={(e) => updateAnimation(anim.id, 'effect', e.target.value)}
                className="text-2xs bg-bg text-ink border border-line rounded-sm px-1 h-6"
              >
                {ANIMATION_EFFECTS.map((e) => <option key={e} value={e}>{e}</option>)}
              </select>
              <button
                type="button"
                onClick={() => removeAnimation(anim.id)}
                className="text-ink-faint hover:text-danger transition-colors flex-shrink-0"
                aria-label="Remove animation"
              >
                <Trash2 size={11} />
              </button>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
