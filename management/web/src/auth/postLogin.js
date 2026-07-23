/**
 * postLogin.js — where does a signed-in user LAND in the management console?
 *
 * In vulos-cloud this resolver reached out to the account's hosted OS box and
 * sent the user to that box's origin. The management console is the control
 * plane's OWN authed surface: a successful sign-in lands in the console
 * dashboard shell (the App layout at the console root).
 *
 * The value returned is an APP-RELATIVE path; the ported pages hand it to
 * goPostLogin() (auth/nav.js), which prefixes the /console basename and does an
 * in-app replace. Kept async to preserve the call-site signature the ported
 * pages expect (they `await resolvePostLoginDestination()`).
 */

/** The console dashboard shell (app-relative — App.jsx renders Dashboard at "/"). */
export const CONSOLE_HOME = '/'

export async function resolvePostLoginDestination() {
  return CONSOLE_HOME
}
