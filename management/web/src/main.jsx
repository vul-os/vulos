import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './theme.css'
import './app.css'
import { AuthProvider } from './auth/AuthProvider.jsx'
import Root from './Root.jsx'

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <AuthProvider>
      <Root />
    </AuthProvider>
  </StrictMode>,
)
