import { createContext, useContext, useState, useCallback, useEffect, type ReactNode } from 'react'
import enCatalog from '../locales/en.json'
import afCatalog from '../locales/af.json'

// A locale catalog is a flat map of translation key -> translated string.
type Catalog = Record<string, string>

// ─── Catalog registry ────────────────────────────────────────────────────────
const CATALOGS: Record<string, Catalog> = {
  en: enCatalog,
  af: afCatalog,
}

// Fallback to 'en' for any locale without a catalog
function resolveCatalog(locale: string): Catalog {
  return CATALOGS[locale] || CATALOGS.en
}

interface I18nContextValue {
  locale: string
  setLocale: (code: string) => void
  t: (key: string, vars?: Record<string, unknown>) => string
}

// ─── Context ─────────────────────────────────────────────────────────────────
const I18nContext = createContext<I18nContextValue | null>(null)

interface I18nProviderProps {
  children?: ReactNode
  initialLocale?: string
}

// ─── Provider ─────────────────────────────────────────────────────────────────
export function I18nProvider({ children, initialLocale }: I18nProviderProps) {
  const [locale, setLocaleState] = useState(() => {
    // Priority: prop from profile > browser language > 'en'
    if (initialLocale && CATALOGS[initialLocale]) return initialLocale
    const browserLang = navigator.language?.split('-')[0]
    if (browserLang && CATALOGS[browserLang]) return browserLang
    return 'en'
  })

  const [catalog, setCatalog] = useState<Catalog>(() => resolveCatalog(locale))

  // Sync catalog when locale changes
  useEffect(() => {
    setCatalog(resolveCatalog(locale))
  }, [locale])

  const setLocale = useCallback((code: string) => {
    setLocaleState(code)
  }, [])

  // Translation function — returns key as fallback so missing keys are visible
  const t = useCallback((key: string, vars?: Record<string, unknown>) => {
    const str = catalog[key] ?? CATALOGS.en[key] ?? key
    if (!vars) return str
    // Simple {{var}} substitution
    return str.replace(/\{\{(\w+)\}\}/g, (_match, name: string) =>
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
// eslint-disable-next-line react-refresh/only-export-components
export function useI18n(): I18nContextValue {
  const ctx = useContext(I18nContext)
  if (!ctx) throw new Error('useI18n must be used within an I18nProvider')
  return ctx
}
