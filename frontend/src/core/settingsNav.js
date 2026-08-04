// settingsNav.js — tiny, race-free deep-link seam for the Settings app.
//
// Settings is a singleton builtin window. When a command/toast wants to open it
// to a specific section (e.g. openSettings('notifications')), it stashes the
// section here; the builtin factory reads + clears it as the initial section on
// mount. Avoids threading props through the generic openWindow/launchApp path.

let pendingSection = null

export function setPendingSettingsSection(section) {
  pendingSection = section || null
}

/** Read and clear the pending section (call once, on Settings mount). */
export function consumePendingSettingsSection() {
  const s = pendingSection
  pendingSection = null
  return s
}
