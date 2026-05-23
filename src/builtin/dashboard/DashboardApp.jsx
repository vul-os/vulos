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
  <div className="flex items-center justify-center h-full bg-neutral-950 text-neutral-600 text-xs gap-2">
    <span className="w-3.5 h-3.5 rounded-full border-2 border-neutral-700 border-t-blue-500 animate-spin" />
    Loading...
  </div>
)

export default function DashboardApp({ initialTab }) {
  const [activeTab, setActiveTab] = useState(initialTab || 'web')

  return (
    <div className="flex flex-col h-full bg-neutral-950 overflow-hidden">
      {/* Tab bar */}
      <div className="flex-shrink-0 flex border-b border-neutral-800/60 bg-neutral-950 px-4 pt-2">
        {TABS.map(tab => (
          <button
            key={tab.id}
            onClick={() => setActiveTab(tab.id)}
            className={`px-4 py-2 text-xs font-medium rounded-t-lg transition-colors border-b-2 -mb-px ${
              activeTab === tab.id
                ? 'text-neutral-100 border-blue-500 bg-neutral-900/60'
                : 'text-neutral-500 border-transparent hover:text-neutral-300 hover:bg-neutral-800/30'
            }`}
          >
            {tab.label}
          </button>
        ))}
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
