/**
 * Phone — the SAME surface as Contacts, not a second one.
 *
 * This app used to be its own thing: Recents / Contacts / Keypad / Messages,
 * with a read-only copy of the address book it fetched itself. That made two
 * surfaces over one address book — two lists, two fetches, two sets of empty
 * states, and a user's answer to "where do I call someone from?" depending on
 * which icon they happened to click. The founder asked for calls to live WITH
 * contacts rather than beside them, so the merged surface lives in
 * builtin/contacts/Contacts.tsx and this is a re-export of it.
 *
 * A re-export rather than a copy is the whole point: two components that agree
 * today are two components that will disagree later, and the phone's contacts
 * tab had ALREADY diverged from the real address book (it could not edit,
 * showed different empty states, and read a different endpoint).
 *
 * TO THE SHELL THIS IS STILL A SECOND APP. `vulos-phone` and `vulos-contacts`
 * are both registered in core/AppRegistry.ts and shell/builtinApps.tsx, which
 * are not this agent's files to edit — so launching either now opens the same
 * merged surface, and the duplicate launcher ENTRY is still there. Retiring
 * that entry is a one-line registry change and has been reported upstream
 * rather than made here.
 */
export { default } from '../contacts/Contacts'
