import { useEffect } from 'react'
import { Routes, Route, Navigate, useLocation } from 'react-router-dom'
import { useAuthStore } from './store/authStore'
import LoginScreen from './components/LoginScreen'
import Layout from './components/Layout'
import Home from './components/Home'
import AppHome from './components/AppHome'
import DocsEditor from './apps/docs/DocsEditor'
import SheetsEditor from './apps/sheets/SheetsEditor'
import SlidesEditor from './apps/slides/SlidesEditor'
import PDFEditor from './apps/pdf/PDFEditor'
import SigningSetup from './apps/pdf/SigningSetup'
import Settings from './components/Settings'
import SignView from './apps/pdf/SignView'
import EnvelopeDashboard from './components/EnvelopeDashboard'
import SpacesApp from './apps/spaces/SpacesApp'
import Meetings from './apps/spaces/Meetings'
import Room from './apps/spaces/Room'
import Verify from './components/Verify'
import CalendarApp from './apps/calendar/CalendarApp'
import ContactsApp from './apps/contacts/ContactsApp'

// Public routes that bypass Vulos auth entirely.
// External signers, external meeting invitees, and external verifiers have no Vulos account.
const PUBLIC_PREFIXES = ['/sign/', '/room/', '/verify']

function isPublicRoute(pathname) {
  return PUBLIC_PREFIXES.some((p) => pathname.startsWith(p))
}

export default function App() {
  const { status, loading, fetchStatus } = useAuthStore()
  const location = useLocation()

  useEffect(() => { fetchStatus() }, [])

  // Render public routes immediately — no auth check, no Layout shell.
  if (isPublicRoute(location.pathname)) {
    return (
      <Routes>
        <Route path="/sign/:token" element={<SignView />} />
        <Route path="/room/:sessionId" element={<Room />} />
        <Route path="/verify" element={<Verify />} />
      </Routes>
    )
  }

  if (loading) {
    return (
      <div className="h-screen flex items-center justify-center bg-bg">
        <div className="w-7 h-7 border-2 border-accent border-t-transparent rounded-full animate-spin" />
      </div>
    )
  }

  if (status?.enabled && !status?.authenticated) {
    return <LoginScreen />
  }

  return (
    <Layout>
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/docs" element={<AppHome type="doc" />} />
        <Route path="/sheets" element={<AppHome type="sheet" />} />
        <Route path="/slides" element={<AppHome type="slide" />} />
        <Route path="/docs/:id" element={<DocsEditor />} />
        <Route path="/sheets/:id" element={<SheetsEditor />} />
        <Route path="/slides/:id" element={<SlidesEditor />} />
        <Route path="/pdf" element={<AppHome type="pdf" />} />
        <Route path="/pdf-editor" element={<PDFEditor />} />
        <Route path="/signing-setup" element={<SigningSetup />} />
        <Route path="/envelopes" element={<EnvelopeDashboard />} />
        <Route path="/spaces" element={<SpacesApp />} />
        <Route path="/spaces/:channelId" element={<SpacesApp />} />
        <Route path="/meetings" element={<Meetings />} />
        <Route path="/room/:sessionId" element={<Room />} />
        <Route path="/calendar" element={<CalendarApp />} />
        <Route path="/contacts" element={<ContactsApp />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Layout>
  )
}
