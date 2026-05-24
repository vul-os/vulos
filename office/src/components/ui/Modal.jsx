/**
 * Modal — centred dialog with soft scrim and a quiet open animation.
 *
 * Notes
 * -----
 * - Closes on backdrop click (`onClose`) and on Escape (Esc).
 * - Uses our `scale-in` keyframe + ease-spring for a calm appear motion.
 * - No portal — Vulos Office is a single-root app and modals are scoped to it.
 *
 * Composition:
 *   <Modal open={…} onClose={…} title="…">
 *     <Modal.Body>…</Modal.Body>
 *     <Modal.Footer>…</Modal.Footer>
 *   </Modal>
 */

import { useEffect } from 'react'
import { X } from 'lucide-react'
import IconButton from './IconButton'

function Modal({ open, onClose, title, size = 'md', children, className = '' }) {
  useEffect(() => {
    if (!open) return
    function onKey(e) { if (e.key === 'Escape') onClose?.() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null

  const sizeMap = {
    sm: 'max-w-sm',
    md: 'max-w-md',
    lg: 'max-w-lg',
    xl: 'max-w-xl',
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center p-4 animate-fade-in"
      onClick={(e) => { if (e.target === e.currentTarget) onClose?.() }}
      style={{ background: 'rgba(26, 25, 22, 0.36)', backdropFilter: 'blur(2px)' }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={`bg-paper text-ink rounded-xl border border-line shadow-e3 w-full ${sizeMap[size] || sizeMap.md} overflow-hidden animate-scale-in ${className}`}
      >
        {title && (
          <div className="flex items-center justify-between px-5 py-3.5 border-b border-line">
            <h3 className="text-md font-semibold tracking-tightish">{title}</h3>
            <IconButton size="sm" title="Close" onClick={onClose}>
              <X size={15} />
            </IconButton>
          </div>
        )}
        {children}
      </div>
    </div>
  )
}

Modal.Body = function ModalBody({ className = '', children }) {
  return <div className={`px-5 py-4 ${className}`}>{children}</div>
}

Modal.Footer = function ModalFooter({ className = '', children }) {
  return (
    <div className={`px-5 py-3 border-t border-line bg-bg-elev2 flex items-center justify-end gap-2 ${className}`}>
      {children}
    </div>
  )
}

export default Modal
