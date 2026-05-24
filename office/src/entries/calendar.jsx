/**
 * src/entries/calendar.jsx — entry point for calendar.vulos.org (dist-calendar/)
 *
 * Mounts CalendarShell with BrowserRouter for history-API deep linking.
 * The backend must serve index.html for all unmatched paths (SPA fallback).
 */

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import CalendarShell from '../shells/CalendarShell.jsx'
import '../index.css'

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <BrowserRouter>
      <CalendarShell />
    </BrowserRouter>
  </StrictMode>
)
