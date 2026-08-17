/**
 * Contacts — people, calling and call history on ONE surface.
 *
 * WHAT THIS REPLACES. Vulos shipped two overlapping surfaces. `vulos-contacts`
 * was the address book: full CRUD over lilmail's cards, with a Call button
 * added to each phone number. `vulos-phone` was a dialler that carried its OWN
 * read-only copy of the address book (a second contacts list, a second fetch, a
 * second set of empty states) plus Recents, a keypad and SMS. Two surfaces over
 * one address book is how two surfaces drift, and it made "call someone" a
 * question of which app you happened to have open.
 *
 * They are now one component, and PEOPLE is the front page. Calling is an
 * action ON a contact rather than a place you go; Recents, the keypad and the
 * SMS inbox are the pages behind it. Both app ids render this — see
 * builtin/phone/Phone.tsx, which is now a re-export, so there is exactly one
 * implementation rather than two that agree today.
 *
 * NO HARDWARE IS THE NORMAL CASE. Most Vulos boxes have no modem, and this
 * surface has to be a good address book on those. Nothing about the people
 * pane is conditional on a radio. Pages that could only ever be empty without
 * one (keypad, messages) are not offered at all rather than offered-and-broken;
 * Recents stays, because that is where a box with no radio is told what to plug
 * in, and because Vulos-to-Vulos call history has nothing to do with GSM.
 *
 * NOTHING HERE IS FAKED FOR THE ABSENT MODEM. Capability is read from the box
 * (GET /api/telephony/status) and a call in progress is read from the modem
 * (GET /api/telephony/call/active). A dial request is never treated as evidence
 * that a call exists. The machine this was written on has no modem, so the
 * hardware paths are exercised against a fake mmcli and a mocked box, never
 * against a radio.
 */

import { useState, useCallback, useMemo } from 'react'
import { LineBar, TopTabs, BottomTabs } from '../phone/PhoneChrome'
import { useSize, visibleTabs, DEFAULT_TAB, type TabId } from '../phone/phoneLayout'
import { useLines, useContacts, useCalls } from '../phone/usePhoneData'
import { useCallSession } from '../phone/useCallSession'
import { digitKey } from '../phone/telephonyApi'
import InCallBar from '../phone/InCallBar'
import RecentsTab from '../phone/RecentsTab'
import Keypad from '../phone/Keypad'
import MessagesTab from '../phone/MessagesTab'
import PeopleView from './PeopleView'

export default function Contacts() {
  const [size, sizeRef] = useSize()
  const [tab, setTab] = useState<TabId>(DEFAULT_TAB)
  const [composeTo, setComposeTo] = useState('')

  const lines = useLines()
  const session = useCallSession()
  // THE box's unified address book (CardDAV + box SIM + pushed phone book),
  // read ONCE here and used for both jobs it is good for: putting names on
  // call and SMS rows, and telling the people pane where each person also
  // lives. It was read twice — once here and once inside the people pane —
  // which is precisely the duplication that merging the two apps set out to
  // remove. Editing still goes to lilmail's cards, which the people pane owns.
  const book = useContacts()
  const calls = useCalls(lines.active?.id ?? null, book.names)

  const active = lines.active
  const canSms = !!active?.sms
  const tabs = useMemo(() => visibleTabs(lines.lines.length > 0), [lines.lines.length])

  // A tab that stops existing (the modem was unplugged) must not leave the app
  // rendering a page that is no longer offered — with no tab highlighted and no
  // way back except a tab the user cannot see.
  const current: TabId = tabs.some((t) => t.id === tab) ? tab : DEFAULT_TAB

  const onCall = useCallback((number: string) => { void session.call(number) }, [session])

  const onMessage = useCallback((number: string) => {
    setComposeTo(number)
    setTab('messages')
  }, [])

  // The far end of a live call, named from the address book when we know them.
  // Falls back to the number inside InCallBar — a withheld number is normal on
  // a real network and must read as "Unknown", not as a blank.
  const activeName = session.active ? book.names.get(digitKey(session.active.number)) : undefined

  const bottomTabs = size === 'narrow'
  const narrow = size === 'narrow'

  const body =
    current === 'contacts' ? (
      <PeopleView session={session} unified={book.contacts} unifiedError={book.error} narrow={narrow} />
    ) : current === 'recents' ? (
      <RecentsTab
        calls={calls.calls} loading={calls.loading || lines.loading} error={calls.error} size={size}
        canCall={session.canCall} callBlockedReason={session.blockedReason}
        hasLine={lines.lines.length > 0} serviceError={lines.serviceError}
        onCall={onCall} onMessage={onMessage} onRetry={calls.reload} onRetryLines={lines.reload} />
    ) : current === 'keypad' ? (
      <Keypad
        contacts={book.contacts} canCall={session.canCall} callBlockedReason={session.blockedReason} canSms={canSms}
        onCall={onCall} onMessage={onMessage} dialError={session.error} />
    ) : (
      <MessagesTab
        lineId={active?.id ?? null} size={size} names={book.names} canSms={canSms}
        composeTo={composeTo} onComposeToConsumed={() => setComposeTo('')} />
    )

  return (
    // Both data hooks are kept: `data-contacts-app` is what the address-book
    // specs select on and `data-phone-app` is what the telephony specs select
    // on, and this one element is now genuinely both.
    <div ref={sizeRef} data-contacts-app data-phone-app data-phone-size={size}
      className="h-full flex flex-col min-h-0"
      style={{ background: 'var(--bg-surface)', color: 'var(--text-primary)' }}>
      {session.active && (
        <InCallBar call={session.active} name={activeName}
          onHangUp={() => { void session.hangup() }}
          onAnswer={() => { void session.answer() }}
          onDecline={() => { void session.decline() }} />
      )}
      <LineBar lines={lines.lines} active={active} onPick={lines.setActive} />
      {!bottomTabs && tabs.length > 1 && <TopTabs tab={current} onPick={setTab} tabs={tabs} />}
      <div className="flex-1 min-h-0">{body}</div>
      {bottomTabs && tabs.length > 1 && <BottomTabs tab={current} onPick={setTab} tabs={tabs} />}
    </div>
  )
}
