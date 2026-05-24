/**
 * src/entries/talk.jsx — entry point for talk.vulos.org (dist-talk/)
 *
 * Mounts TalkShell with BrowserRouter for history-API deep linking.
 * The backend must serve index.html for all unmatched paths (SPA fallback).
 */

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import TalkShell from '../shells/TalkShell.jsx'
import '../index.css'

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <BrowserRouter>
      <TalkShell />
    </BrowserRouter>
  </StrictMode>
)
