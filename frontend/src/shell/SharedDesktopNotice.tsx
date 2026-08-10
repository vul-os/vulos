import { useEffect, useState } from 'react'
import { useShell } from '../providers/ShellProvider'

// SharedDesktopNotice — tell the user when a second window is on this desktop,
// and make taking their own the easy path.
//
// # Why this exists at all
//
// Two tabs used to silently overwrite each other's desktop: one localStorage
// key, a 500ms debounce, and no coordination, so whichever tab saved last won
// and the other's windows vanished on reload. That is fixed underneath — one
// tab is elected WRITER and the others mirror it, so sharing genuinely works
// now.
//
// But "works" is not the same as "what you wanted". Two windows showing the
// same desktop is occasionally deliberate (a second screen, someone watching)
// and usually accidental (a bookmark opened twice). The accidental case is
// confusing in a specific way: you drag a window in the follower tab and it
// snaps back, because the writer's state is authoritative. Nothing is broken,
// yet nothing behaves as expected either.
//
// So the notice explains what is happening in one line and offers the thing the
// user probably wanted. It does NOT force a new desktop: sharing is legitimate,
// it is now safe, and taking a decision away from someone to protect them from
// a state they may have chosen deliberately is worse than explaining it.
//
// # Why it is not a modal
//
// A modal would block the shell on a condition that is frequently intentional
// and always recoverable. This is a dismissible banner: it appears when a peer
// is genuinely present, disappears when they leave, and never traps focus.

export default function SharedDesktopNotice() {
  const { session, addDesktop, desktops } = useShell()
  const [dismissed, setDismissed] = useState(false)

  const peerCount = session?.peers?.length ?? 0
  const isFollower = session?.role === 'follower'

  // Re-arm when the peer goes away and comes back: a dismissal applies to the
  // situation the user dismissed, not to every future one. Without this,
  // dismissing once would hide a genuinely new second window an hour later.
  useEffect(() => {
    if (peerCount === 0) setDismissed(false)
  }, [peerCount])

  if (peerCount === 0 || dismissed) return null

  const takeOwnDesktop = () => {
    // addDesktop switches to the new desktop as it creates it, which also moves
    // this tab off the contested one — so it stops being a follower and starts
    // owning its own state, in one action.
    addDesktop(`Desktop ${Object.keys(desktops).length + 1}`)
    setDismissed(true)
  }

  return (
    <div
      role="status"
      className="fixed left-1/2 -translate-x-1/2 z-[70] flex items-center gap-3 rounded-xl border px-4 py-2.5 shadow-lg"
      style={{
        // Below the menu bar rather than over it: this is information, not an
        // interruption, and it must not cover the clock or the trust badge.
        top: 'calc(var(--menubar-h, 28px) + 10px)',
        background: 'var(--bg-elevated)',
        borderColor: 'var(--border-strong)',
        maxWidth: 'min(560px, calc(100vw - 32px))',
      }}
    >
      <span
        aria-hidden="true"
        className="shrink-0 w-2 h-2 rounded-full"
        style={{ background: 'var(--accent)' }}
      />
      <p className="text-[13px] leading-snug min-w-0" style={{ color: 'var(--text-secondary)' }}>
        <span style={{ color: 'var(--text-primary)', fontWeight: 600 }}>
          {peerCount === 1 ? 'Another window' : `${peerCount} other windows`}
        </span>
        {' is on this desktop. '}
        {isFollower
          // Said plainly, because this is the confusing case: the user's drags
          // will snap back and they deserve to know why before it happens.
          ? 'Changes are driven by the window that opened it first — yours will follow along.'
          : 'You are driving; the others follow what you do here.'}
      </p>
      <div className="flex items-center gap-1.5 shrink-0">
        <button
          onClick={takeOwnDesktop}
          className="btn-primary text-[12px] px-2.5 py-1"
        >
          Give me my own
        </button>
        {/* No aria-label here: the visible text IS the accessible name. An
            earlier version carried aria-label="Dismiss", which overrides the
            label computation — so a screen reader announced "Dismiss" while the
            screen said "Keep sharing", and the two disagreed about what the
            button does. Caught by the test querying it by its visible name. */}
        <button
          onClick={() => setDismissed(true)}
          className="text-[12px] px-2 py-1 rounded-lg"
          style={{ color: 'var(--text-tertiary)' }}
        >
          Keep sharing
        </button>
      </div>
    </div>
  )
}
