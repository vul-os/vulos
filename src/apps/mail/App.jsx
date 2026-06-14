import { useState, useEffect } from 'react'

// Vulos OS Mail app.
//
// The default mail client is LilMail (github.com/exolutionza/lilmail) — a
// lightweight, database-free IMAP/SMTP webmail client, part of the Vula OS
// suite. It runs as a local service; the OS embeds it same-window via an
// iframe. The service URL is provided by the backend (VULOS_MAIL_URL),
// defaulting to http://localhost:3000.
//
// LilMail must be configured with `frame_ancestors` covering this OS origin so
// the shell is permitted to embed it (see lilmail config.toml).
export default function MailApp() {
  const [url, setUrl] = useState(null)

  useEffect(() => {
    let alive = true
    fetch('/api/mail/url')
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('mail service unavailable'))))
      .then((d) => { if (alive) setUrl(d.url || 'http://localhost:3000') })
      .catch(() => { if (alive) setUrl('http://localhost:3000') })
    return () => { alive = false }
  }, [])

  if (!url) {
    return (
      <div className="w-full h-full flex items-center justify-center text-neutral-500 text-sm">
        Connecting to Mail…
      </div>
    )
  }

  return (
    <iframe
      src={url}
      title="Mail"
      className="w-full h-full border-0"
      sandbox="allow-scripts allow-forms allow-popups allow-same-origin allow-downloads"
      referrerPolicy="no-referrer"
    />
  )
}
