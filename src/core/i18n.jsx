import { createContext, useContext, useState, useCallback, useEffect } from 'react'
import enCatalog from '../locales/en.json'
import afCatalog from '../locales/af.json'

// ─── Catalog registry ────────────────────────────────────────────────────────
const CATALOGS = {
  en: enCatalog,
  af: afCatalog,
}

// Fallback to 'en' for any locale without a catalog
function resolveCatalog(locale) {
  return CATALOGS[locale] || CATALOGS.en
}

// ─── Context ─────────────────────────────────────────────────────────────────
const I18nContext = createContext(null)

// ─── Provider ─────────────────────────────────────────────────────────────────
export function I18nProvider({ children, initialLocale }) {
  const [locale, setLocaleState] = useState(() => {
    // Priority: prop from profile > browser language > 'en'
    if (initialLocale && CATALOGS[initialLocale]) return initialLocale
    const browserLang = navigator.language?.split('-')[0]
    if (browserLang && CATALOGS[browserLang]) return browserLang
    return 'en'
  })

  const [catalog, setCatalog] = useState(() => resolveCatalog(locale))

  // Sync catalog when locale changes
  useEffect(() => {
    setCatalog(resolveCatalog(locale))
  }, [locale])

  const setLocale = useCallback((code) => {
    setLocaleState(code)
  }, [])

  // Translation function — returns key as fallback so missing keys are visible
  const t = useCallback((key, vars) => {
    const str = catalog[key] ?? enCatalog[key] ?? key
    if (!vars) return str
    // Simple {{var}} substitution
    return str.replace(/\{\{(\w+)\}\}/g, (_, name) =>
      vars[name] !== undefined ? String(vars[name]) : `{{${name}}}`
    )
  }, [catalog])

  return (
    <I18nContext.Provider value={{ locale, setLocale, t }}>
      {children}
    </I18nContext.Provider>
  )
}

// ─── Hook ─────────────────────────────────────────────────────────────────────
export function useI18n() {
  const ctx = useContext(I18nContext)
  if (!ctx) throw new Error('useI18n must be used within an I18nProvider')
  return ctx
}
