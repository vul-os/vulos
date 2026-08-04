/**
 * DashboardApp — MINST-07
 * Tabbed dashboard wrapper: "Web" (AppPublishCard, PUBWEB-05) + "Instances" (InstancesPanel, MINST-07).
 */
import { useState, Suspense, lazy } from 'react'

const AppPublishCard = lazy(() => import('./AppPublishCard'))
const InstancesPanel = lazy(() => import('./InstancesPanel'))

const TABS = [
  { id: 'web', label: 'Web' },
  { id: 'instances', label: 'Instances' },
]

const Spinner = () => (
  <div className="flex items-center justify-center h-full bg-[var(--bg-base)] text-[var(--text-muted)] text-xs gap-2">
    <span className="w-3.5 h-3.5 spinner" />
    Loading...
  </div>
)

export default function DashboardApp({ initialTab }) {
  const [activeTab, setActiveTab] = useState(initialTab || 'web')

  return (
    <div className="flex flex-col h-full bg-[var(--bg-base)] text-[var(--text-primary)] overflow-hidden">
      {/* Tab bar */}
      <div className="flex-shrink-0 flex items-center gap-1 border-b border-[var(--border-default)] bg-[var(--bg-surface)]/60 px-3 pt-2.5 pb-0">
        {TABS.map(tab => {
          const on = activeTab === tab.id
          return (
            <button
              key={tab.id}
              onClick={() => setActiveTab(tab.id)}
              aria-pressed={on}
              className={`relative px-3.5 py-2 text-[13px] font-medium rounded-t-lg transition-colors ${
                on
                  ? 'text-[var(--text-primary)]'
                  : 'text-[var(--text-tertiary)] hover:text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]/50'
              }`}
            >
              {tab.label}
              <span
                aria-hidden="true"
                className={`absolute left-2 right-2 -bottom-px h-0.5 rounded-full transition-all ${on ? 'accent-bg opacity-100' : 'opacity-0'}`}
              />
            </button>
          )
        })}
      </div>

      {/* Tab panels */}
      <div className="flex-1 overflow-hidden">
        {activeTab === 'web' && (
          <Suspense fallback={<Spinner />}>
            <AppPublishCard />
          </Suspense>
        )}
        {activeTab === 'instances' && (
          <Suspense fallback={<Spinner />}>
            <InstancesPanel />
          </Suspense>
        )}
      </div>
    </div>
  )
}
