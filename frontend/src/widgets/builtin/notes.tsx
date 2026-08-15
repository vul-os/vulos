// notes.tsx — a scratchpad that lives on the desktop.
//
// The simplest possible demonstration that `storage` is a real capability: type
// something, it is still there after a reload, and it never leaves the box.
// There is no notes backend, no sync and no account — the text is in this
// widget instance's own namespace and nowhere else, which is exactly what a
// desktop scratchpad should be.
//
// It also exercises the "storage denied" path, which every widget with a
// capability has to have: without the grant it renders an explanation rather
// than a text box that silently forgets everything.
import { useEffect, useRef, useState } from 'react'
import {
  defineWidget, registerWidget,
  WidgetFrame, WidgetTitle, WidgetEmpty, WidgetLabel,
  type WidgetContext, type WidgetStorage,
} from '../index'

const KEY = 'text'
const SAVE_DEBOUNCE_MS = 400

function NotesBody({ storage, size }: { storage: WidgetStorage; size: string }) {
  const [text, setText] = useState(() => storage.get(KEY) ?? '')
  const [full, setFull] = useState(true)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Debounced, because localStorage writes are synchronous and this fires on
  // every keystroke. A quota refusal is surfaced rather than swallowed — a
  // scratchpad that silently stops saving is worse than one that says it did.
  useEffect(() => {
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(() => { setFull(storage.set(KEY, text)) }, SAVE_DEBOUNCE_MS)
    return () => { if (timer.current) clearTimeout(timer.current) }
  }, [text, storage])

  return (
    <>
      <WidgetTitle right={!full ? <span className="vwidget-tone-warning">full</span> : undefined}>Notes</WidgetTitle>
      <textarea
        className="vwidget-input"
        style={{ flex: '1 1 auto', minHeight: size === 'large' ? '112px' : '40px', resize: 'none', lineHeight: 1.45 }}
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder="Scratch notes — stays on this box"
        aria-label="Notes"
        spellCheck={false}
      />
      {!full && <WidgetLabel tone="warning">Out of room — shorten this note to keep saving.</WidgetLabel>}
    </>
  )
}

export default function NotesWidget(ctx: WidgetContext) {
  if (!ctx.storage) {
    return (
      <WidgetFrame title="Notes">
        <WidgetTitle>Notes</WidgetTitle>
        <WidgetEmpty>Allow “Store its own settings” so this note can be kept.</WidgetEmpty>
      </WidgetFrame>
    )
  }
  return (
    <WidgetFrame title="Notes">
      <NotesBody storage={ctx.storage} size={ctx.size} />
    </WidgetFrame>
  )
}

registerWidget(defineWidget({
  manifest: {
    id: 'vulos.notes',
    name: 'Notes',
    description: 'A scratchpad on the desktop. Stored on this box only.',
    version: '1.0.0',
    author: 'Vulos',
    sizes: ['medium', 'large'],
    tick: 'none',
    permissions: ['storage'],
  },
  render: (ctx) => <NotesWidget {...ctx} />,
}))
