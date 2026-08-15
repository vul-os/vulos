/**
 * WidgetsPanel.tsx — every grant every widget holds, in one place.
 *
 * The widget system ships a real default-deny permission model: a manifest
 * REQUESTS permissions, the user GRANTS them per placement, and until a grant
 * exists the corresponding field of WidgetContext is null. What it did not ship
 * was anywhere to see the result. Grants were made one at a time, at the moment
 * a widget was added, and after that they were invisible — so "what can the
 * things on my desktop actually do" had no answer short of reading
 * localStorage.
 *
 * This panel is that answer, and it is deliberately a REVIEW surface rather
 * than another place to add widgets:
 *
 *   • every placement, with what it holds and what it asked for and did not get,
 *   • which grants can move data off the box (PERMISSION_INFO.leaves),
 *   • the exact hosts a network-granted widget may reach, from its manifest,
 *   • revoke, per permission, per placement.
 *
 * Revoking writes through setGrants(), which re-filters against the manifest's
 * requested set, so this panel cannot grant a permission the widget never asked
 * for even if the stored layout is edited by hand.
 */
import { useCallback, useState } from 'react'
import { Section, Card, EmptyState, Banner, Pill, SettingRow, Toggle } from './ui'
import { loadLayout, saveLayout, setGrants } from '../../widgets/layout'
import { getWidget } from '../../widgets/registry'
import { PERMISSION_INFO, type WidgetLayout, type WidgetPermission } from '../../widgets/types'

export default function WidgetsPanel() {
  // Lazy initial state rather than a load-in-effect: the layout comes from
  // localStorage, which is available at first render in this client-only shell,
  // and setting state inside an effect just to seed it triggers a second render
  // for no benefit.
  const [layout, setLayout] = useState<WidgetLayout>(() => loadLayout())

  // Revoking is the only write here. It goes through setGrants() rather than
  // assigning `granted` so the manifest stays the upper bound on what any
  // placement can hold.
  const toggle = useCallback((instanceId: string, perm: WidgetPermission, on: boolean) => {
    setLayout((prev) => {
      const inst = prev.instances.find((i) => i.instanceId === instanceId)
      if (!inst) return prev
      const next = on
        ? [...inst.granted, perm]
        : inst.granted.filter((p) => p !== perm)
      const updated = setGrants(prev, instanceId, next)
      saveLayout(updated)
      return updated
    })
  }, [])

  const instances = layout.instances

  return (
    <Section
      icon="widgets"
      title="Widgets"
      desc="What each widget on your desktop is allowed to do, and what it asked for."
    >
      {instances.length === 0 && (
        <EmptyState
          icon="widgets"
          title="No widgets placed"
          hint="Add a widget to the desktop rail and its permissions will be listed here."
        />
      )}

      {instances.map((inst) => {
        const def = getWidget(inst.widgetId)
        const manifest = def?.manifest
        const requested = manifest?.permissions ?? []
        const granted = new Set(inst.granted)
        const leaks = inst.granted.some((p) => PERMISSION_INFO[p]?.leaves)

        return (
          <Card
            key={inst.instanceId}
            title={manifest?.name || inst.widgetId}
            desc={manifest?.description}
            aside={
              <Pill tone={leaks ? 'warning' : 'neutral'}>
                {leaks ? 'Can reach off-box' : 'Stays on this box'}
              </Pill>
            }
          >
            {/* A widget whose code is not registered cannot be reasoned about,
                and saying nothing would imply it holds nothing. */}
            {!manifest && (
              <Banner tone="warning" title="This widget is not installed">
                It is still placed on the rail and still holds the grants below.
                Remove it from the rail if you did not expect it.
              </Banner>
            )}

            {requested.length === 0 && manifest && (
              <p className="text-sm text-[var(--text-muted)]">
                This widget requests no permissions at all.
              </p>
            )}

            {requested.map((perm) => {
              const info = PERMISSION_INFO[perm]
              const on = granted.has(perm)
              return (
                <SettingRow
                  key={perm}
                  label={info?.title || perm}
                  desc={
                    // The denied case is stated explicitly. A row that merely
                    // showed an off switch would read as "not used yet" rather
                    // than "asked for this and was refused".
                    on
                      ? info?.detail
                      : `Requested, not granted. ${info?.detail ?? ''}`
                  }
                  control={
                    <div className="flex items-center gap-2.5">
                      {info?.leaves && (
                        <Pill tone={on ? 'warning' : 'neutral'}>Leaves the box</Pill>
                      )}
                      <Toggle
                        ariaLabel={`${info?.title || perm} for ${manifest?.name || inst.widgetId}`}
                        checked={on}
                        onChange={(v) => toggle(inst.instanceId, perm, v)}
                      />
                    </div>
                  }
                />
              )
            })}

            {/* Hosts are shown whenever the widget REQUESTS network, granted or
                not: "who would this talk to if I said yes" is the question a
                user is actually asking at the moment they decide. */}
            {requested.includes('network') && (
              <div className="mt-3">
                <p className="text-[12px] uppercase tracking-wider text-[var(--text-tertiary)] mb-1">
                  Hosts it may reach
                </p>
                {manifest?.hosts?.length ? (
                  <ul className="text-sm text-[var(--text-secondary)] space-y-0.5">
                    {manifest.hosts.map((h) => (
                      <li key={h} className="font-mono text-[12px]">{h}</li>
                    ))}
                  </ul>
                ) : (
                  <p className="text-sm text-[var(--text-muted)]">None declared.</p>
                )}
              </div>
            )}
          </Card>
        )
      })}
    </Section>
  )
}
