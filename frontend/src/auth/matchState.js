// matchState — pure derivation of a confirm-field's match status, shared by the
// setup wizard's confirm-password and confirm-PIN inline feedback (the WAVE-26
// papercut fix that pairs the red/green border with accessible text). `touched`
// gates any feedback until the user has typed into the confirm field.
export function matchState(value, confirm) {
  const touched = (confirm || '').length > 0
  return {
    touched,
    matches: touched && value === confirm,
    mismatch: touched && value !== confirm,
  }
}
