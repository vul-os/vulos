// WidgetConfig.tsx — the OS-drawn settings + permissions panel for one placement.
//
// THE WIDGET DOES NOT DRAW THIS. A widget declares its settings in its manifest
// and the host renders the form. That is a security property, not a convenience:
// if a widget drew its own settings UI it could draw a convincing "Vulos needs
// your password" panel inside the rail, and the user would have no way to tell
// it from the OS. Everything the user types about a widget, they type into a
// form the OS built.
//
// Permissions are per PLACEMENT and toggled here, one at a time, each with the
// sentence from PERMISSION_INFO printed next to it. There is no "allow all".

import { PERMISSION_INFO, type WidgetInstance, type WidgetManifest, type WidgetPermission, type WidgetSettingSpec } from '../types'
import { isProxyKnownAvailable } from '../net'
import './widgets.css'

export default function WidgetConfig({
  manifest, instance, onSetting, onGrants, onClose, proxyProbed,
}: {
  manifest: WidgetManifest
  instance: WidgetInstance
  onSetting: (key: string, value: string | number | boolean | string[]) => void
  onGrants: (granted: WidgetPermission[]) => void
  onClose: () => void
  proxyProbed: boolean
}) {
  const requested = manifest.permissions ?? []
  const toggle = (p: WidgetPermission) => {
    const has = instance.granted.includes(p)
    onGrants(has ? instance.granted.filter((x) => x !== p) : [...instance.granted, p])
  }

  return (
    <div className="vwidget-panel" role="dialog" aria-label={`${manifest.name} settings`}>
      <div className="vwidget-panel-head">
        <span className="vwidget-panel-title">{manifest.name}</span>
        <button type="button" className="vwidget-chip focus-primary" onClick={onClose} aria-label="Close settings">×</button>
      </div>
      <div className="vwidget-panel-body">
        {(manifest.settings ?? []).map((spec) => (
          <Field
            key={spec.key}
            spec={spec}
            value={instance.settings[spec.key]}
            onChange={(v) => onSetting(spec.key, v)}
          />
        ))}

        {requested.length > 0 && (
          <>
            <div className="vwidget-panel-title" style={{ marginTop: '10px', padding: '6px 2px' }}>Permissions</div>
            {requested.map((p) => {
              const info = PERMISSION_INFO[p]
              const on = instance.granted.includes(p)
              return (
                <div key={p} className="vwidget-perm" data-leaves={info.leaves ? 'true' : undefined}>
                  <span className="vwidget-perm-dot" aria-hidden="true" />
                  <span style={{ flex: '1 1 auto', minWidth: 0 }}>
                    <span className="vwidget-perm-title">{info.title}</span>
                    <span className="vwidget-perm-detail">{info.detail}</span>
                    {/* The honest status line for `network`. A user who grants it
                        on a box with no broker must be told that nothing will
                        happen, rather than being left to wonder why the widget
                        still shows no data. */}
                    {p === 'network' && proxyProbed && !isProxyKnownAvailable() && (
                      <span className="vwidget-perm-detail vwidget-tone-warning">
                        This box does not broker widget requests, so nothing will be fetched even if you allow this.
                      </span>
                    )}
                    {p === 'network' && (manifest.hosts ?? []).length > 0 && (
                      <span className="vwidget-perm-detail mono">{(manifest.hosts ?? []).join(', ')}</span>
                    )}
                  </span>
                  <button
                    type="button"
                    className="vwidget-chip focus-primary"
                    data-active={on ? 'true' : undefined}
                    role="switch"
                    aria-checked={on}
                    aria-label={`${info.title}: ${on ? 'allowed' : 'blocked'}`}
                    onClick={() => toggle(p)}
                  >
                    {on ? 'Allowed' : 'Blocked'}
                  </button>
                </div>
              )
            })}
          </>
        )}

        {requested.length === 0 && (manifest.settings ?? []).length === 0 && (
          <div className="vwidget-empty" style={{ padding: '6px 2px' }}>
            <span className="vwidget-empty-text">This widget has no settings and asks for no permissions.</span>
          </div>
        )}
      </div>
    </div>
  )
}

function Field({
  spec, value, onChange,
}: {
  spec: WidgetSettingSpec
  value: unknown
  onChange: (v: string | number | boolean | string[]) => void
}) {
  const id = `vw-${spec.key}`
  return (
    <div className="vwidget-field">
      <label className="vwidget-field-label" htmlFor={id}>{spec.label}</label>
      {spec.type === 'string' && (
        <input
          id={id} className="vwidget-input" type="text"
          value={typeof value === 'string' ? value : ''}
          placeholder={spec.placeholder}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
      {spec.type === 'number' && (
        <input
          id={id} className="vwidget-input" type="number"
          value={typeof value === 'number' ? value : ''}
          min={spec.min} max={spec.max}
          onChange={(e) => onChange(Number(e.target.value))}
        />
      )}
      {spec.type === 'boolean' && (
        <button
          type="button" id={id} className="vwidget-chip focus-primary"
          role="switch" aria-checked={value === true}
          data-active={value === true ? 'true' : undefined}
          onClick={() => onChange(value !== true)}
        >
          {value === true ? 'On' : 'Off'}
        </button>
      )}
      {spec.type === 'select' && (
        <select
          id={id} className="vwidget-input"
          value={typeof value === 'string' ? value : spec.options[0].value}
          onChange={(e) => onChange(e.target.value)}
        >
          {spec.options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
        </select>
      )}
      {spec.type === 'list' && (
        // A list is edited as comma-separated text. Deliberately the simplest
        // control that works in a 280px rail: a row-per-item editor with add and
        // remove buttons is a better form and a worse fit, and this is a panel a
        // user opens for ten seconds to add "Tokyo".
        <input
          id={id} className="vwidget-input" type="text"
          value={Array.isArray(value) ? value.join(', ') : ''}
          placeholder={spec.placeholder}
          onChange={(e) => onChange(e.target.value.split(',').map((s) => s.trim()).filter(Boolean))}
        />
      )}
      {spec.help && <span className="vwidget-field-help">{spec.help}</span>}
    </div>
  )
}
