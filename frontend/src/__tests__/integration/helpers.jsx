// Shared helpers for the wave-28 shell integration suite.
//
// These render REAL component subtrees (the command palette, the assistant
// panel, Drive, settings panels, the notification center) wrapped in the REAL
// providers they need, so a test exercises the actual data flow: user event →
// component → fetch → MSW backend → component re-render → assertion. Only the
// network boundary is mocked (via MSW); everything above it is the shipping code.

import { render } from '@testing-library/react'
import { AuthProvider } from '../../auth/AuthProvider'
import { ThemeProvider } from '../../core/ThemeProvider'
import { I18nProvider } from '../../core/i18n'
import { SovereigntyProvider } from '../../core/useSovereignty'

// Wrap a tree in the shell's core context providers. Deliberately does NOT
// mount ShellProvider (which starts websockets/native-window polling) — tests
// that need shell actions pass their own useShell mock or use ShellProvider
// explicitly.
export function withCoreProviders(ui) {
  return (
    <I18nProvider>
      <ThemeProvider>
        <AuthProvider>
          <SovereigntyProvider>{ui}</SovereigntyProvider>
        </AuthProvider>
      </ThemeProvider>
    </I18nProvider>
  )
}

export function renderWithProviders(ui, options) {
  return render(withCoreProviders(ui), options)
}

// Open the command palette by dispatching the global ⌘K keydown the shell
// listens for on window (capture phase).
export function pressCmdK() {
  window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true }))
}
