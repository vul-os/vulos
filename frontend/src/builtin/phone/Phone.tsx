/**
 * Phone — calls, contacts and messages in one app.
 *
 * WHAT CHANGED AND WHY. This app used to talk to exactly one thing: the Android
 * native bridge. On any box that was not an Android handset it rendered "No SIM
 * on this device — Phone only works inside the Vulos Android app", which was
 * simply untrue: the box has had a full GSM service (backend/services/telephony)
 * speaking ModemManager to a USB LTE stick, an M.2 card or any other modem the
 * whole time. That service was reachable only from a separate static app
 * (frontend/apps/phone), so the windowed Phone app in the shell could not see
 * the box's own SIM. Now the box's modem is the FIRST line checked and the
 * default; the handset SIM is one optional extra line among three.
 *
 * The tabs are Recents / Contacts / Keypad / Messages, which is the Android
 * shape the founder asked for: a person and their call history are one surface,
 * and dialling is a first-class act rather than a text field.
 *
 * Contacts here READS the box's unified address book; the standalone Contacts
 * app still owns editing. Two write paths to one book is how two surfaces drift.
 */

import { useState, useCallback } from 'react'
import { useShell } from '../../providers/ShellProvider'
import { getAppById } from '../../core/AppRegistry'
import { launchApp } from '../../shell/launchApp'
import { LineBar, TopTabs, BottomTabs, NoLineState } from './PhoneChrome'
import { useSize, type TabId } from './phoneLayout'
import { useLines, useContacts, useCalls } from './usePhoneData'
import { placeCall } from './telephonyApi'
import { friendlyError } from './phoneUtils'
import { nativeBridge } from '../../core/nativeBridge'
import RecentsTab from './RecentsTab'
import ContactsTab from './ContactsTab'
import Keypad from './Keypad'
import MessagesTab from './MessagesTab'

export default function Phone() {
  const { openWindow } = useShell()
  const [size, sizeRef] = useSize()
  const [tab, setTab] = useState<TabId>('recents')
  const [composeTo, setComposeTo] = useState('')
  const [dialError, setDialError] = useState('')

  const openContactsApp = useCallback(() => {
    const app = getAppById('vulos-contacts')
    if (app) launchApp(app, { openWindow })
  }, [openWindow])

  const lines = useLines()
  const contacts = useContacts()
  const calls = useCalls(lines.active?.id ?? null, contacts.names)

  const active = lines.active
  const canCall = !!active?.voice
  const canSms = !!active?.sms

  // The single place that explains why dialling is off. Every disabled call
  // button shows this, so the reason is never a mystery or a silent no-op.
  const callBlockedReason = !active
    ? 'No line is available on this box — plug in a GSM modem with a SIM.'
    : !active.voice
      ? `${active.label} is data/SMS only — this modem reports no voice support, so it cannot place calls.`
      : ''

  const onCall = useCallback(async (number: string) => {
    setDialError('')
    if (!number) return
    try {
      if (active?.id === 'device') await nativeBridge.telephony.dial(number)
      else await placeCall(number)
      calls.reload()
    } catch (e) {
      // placeCall throws on BOTH a non-2xx and a 200 carrying {"error":…} — the
      // box answers "no modem" with status 200, so res.ok alone would report a
      // call that never happened as a success.
      setDialError(friendlyError(e))
      setTab('keypad')
    }
  }, [active, calls])

  const onMessage = useCallback((number: string) => {
    setComposeTo(number)
    setTab('messages')
  }, [])

  const bottomTabs = size === 'narrow'

  // No line at all: say what to plug in, and still give the user their address
  // book — the contacts on this box are not hardware-dependent and are exactly
  // what someone would want while working out why the modem isn't showing up.
  const body = !active && !lines.loading ? (
    tab === 'contacts' ? (
      <ContactsTab
        contacts={contacts.contacts} loading={contacts.loading} error={contacts.error} size={size}
        canCall={false} callBlockedReason={callBlockedReason} canSms={false}
        onCall={onCall} onMessage={onMessage} onRetry={contacts.reload}
        onOpenContactsApp={openContactsApp} />
    ) : (
      <NoLineState serviceError={lines.serviceError} onRetry={lines.reload}>
        <button type="button" onClick={() => setTab('contacts')}
          className="px-3.5 py-2 rounded-lg text-[13px] font-medium focus-primary transition-colors"
          style={{ background: 'var(--bg-elevated)', color: 'var(--text-primary)', border: '1px solid var(--border-default)' }}>
          Open contacts
        </button>
      </NoLineState>
    )
  ) : tab === 'recents' ? (
    <RecentsTab
      calls={calls.calls} loading={calls.loading || lines.loading} error={calls.error} size={size}
      canCall={canCall} callBlockedReason={callBlockedReason}
      onCall={onCall} onMessage={onMessage} onRetry={calls.reload} />
  ) : tab === 'contacts' ? (
    <ContactsTab
      contacts={contacts.contacts} loading={contacts.loading} error={contacts.error} size={size}
      canCall={canCall} callBlockedReason={callBlockedReason} canSms={canSms}
      onCall={onCall} onMessage={onMessage} onRetry={contacts.reload}
      onOpenContactsApp={openContactsApp} />
  ) : tab === 'keypad' ? (
    <Keypad
      contacts={contacts.contacts} canCall={canCall} callBlockedReason={callBlockedReason} canSms={canSms}
      onCall={onCall} onMessage={onMessage} dialError={dialError} />
  ) : (
    <MessagesTab
      lineId={active?.id ?? null} size={size} names={contacts.names} canSms={canSms}
      composeTo={composeTo} onComposeToConsumed={() => setComposeTo('')} />
  )

  return (
    <div ref={sizeRef} data-phone-app data-phone-size={size}
      className="h-full flex flex-col min-h-0"
      style={{ background: 'var(--bg-surface)', color: 'var(--text-primary)' }}>
      <LineBar lines={lines.lines} active={active} onPick={lines.setActive} />
      {!bottomTabs && <TopTabs tab={tab} onPick={setTab} />}
      <div className="flex-1 min-h-0">{body}</div>
      {bottomTabs && <BottomTabs tab={tab} onPick={setTab} />}
    </div>
  )
}
