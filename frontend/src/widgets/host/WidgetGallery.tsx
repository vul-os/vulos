// WidgetGallery.tsx — "Add widget".
//
// Every entry shows what the widget WANTS before it is added, not after. The
// permissions and the hosts a widget declares are the two facts a user of a
// sovereignty box actually needs, and they are printed on the card rather than
// hidden behind a details toggle. Nothing here grants anything: adding a widget
// grants it nothing at all (see WidgetRail's 'add' case), so this list is
// informational and the decision comes later, per permission, in WidgetConfig.

import { PERMISSION_INFO, type AnyWidgetDefinition } from '../types'
import './widgets.css'

export default function WidgetGallery({
  widgets, onAdd, onClose,
}: {
  widgets: AnyWidgetDefinition[]
  onAdd: (id: string) => void
  onClose: () => void
}) {
  return (
    <div className="vwidget-panel" role="dialog" aria-label="Add a widget">
      <div className="vwidget-panel-head">
        <span className="vwidget-panel-title">Add a widget</span>
        <button type="button" className="vwidget-chip focus-primary" onClick={onClose} aria-label="Close gallery">×</button>
      </div>
      <div className="vwidget-panel-body">
        {widgets.length === 0 ? (
          <div className="vwidget-empty" style={{ padding: '8px' }}>
            <span className="vwidget-empty-text">No widgets are installed on this box.</span>
          </div>
        ) : (
          <ul>
            {widgets.map((w) => {
              const m = w.manifest
              const perms = m.permissions ?? []
              // The one distinction that matters on this product: does adding
              // this widget create a path off the box? Everything else is local.
              const leaves = perms.filter((p) => PERMISSION_INFO[p].leaves)
              return (
                <li key={m.id}>
                  <button
                    type="button"
                    className="vwidget-gallery-item focus-primary"
                    onClick={() => onAdd(m.id)}
                    aria-label={`Add ${m.name}`}
                  >
                    <span className="vwidget-gallery-name">{m.name}</span>
                    <span className="vwidget-gallery-desc">{m.description}</span>
                    <span className="vwidget-gallery-meta">
                      {m.sizes.join(' · ')}
                      {m.author ? ` · ${m.author}` : ''}
                      {perms.length === 0 ? ' · no permissions' : ` · asks for ${perms.length}`}
                    </span>
                    {leaves.length > 0 && (
                      <span className="vwidget-gallery-meta vwidget-tone-warning">
                        Can reach: {(m.hosts ?? []).join(', ') || 'the network'}
                      </span>
                    )}
                  </button>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </div>
  )
}
