/**
 * config.js — deployment constants for the management console SPA.
 *
 * Ported from vulos-cloud so the auth pages that reference the deployment domain
 * (e.g. Forgot.jsx, which explains where a reset link is sent) resolve the same
 * way. Overridable at build time via VITE_VULOS_DOMAIN so a dev/staging build can
 * run on a different apex.
 */

export const VULOS_DOMAIN =
  (import.meta.env && import.meta.env.VITE_VULOS_DOMAIN) || 'vulos.org'

export const SITE_URL = `https://${VULOS_DOMAIN}`
