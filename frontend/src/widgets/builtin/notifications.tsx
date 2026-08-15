// notifications.tsx — a glance at the notification centre, not a second copy of it.
//
// Read-only, on purpose and by permission: this widget holds `notifications`
// (read) and NOT `notify` (write), so it can show what the box said and cannot
// invent an alert. It also carries no bulk actions — the menu-bar bell owns the
// full list, the per-source preferences and "mark all read". A second copy of
// that button made "Mark all read" ambiguous on the page, which was a usability
// problem before a suite ever caught it as a strict-mode violation.
import {
  defineWidget, registerWidget,
  WidgetFrame, WidgetTitle, WidgetLabel, WidgetEmpty,
  type WidgetContext,
} from '../index'

export default function NotificationsWidget(ctx: WidgetContext) {
  const n = ctx.notifications
  if (!n) {
    return (
      <WidgetFrame title="Notifications">
        <WidgetTitle>Notifications</WidgetTitle>
        <WidgetEmpty>Allow “Read your notifications” in this widget’s settings.</WidgetEmpty>
      </WidgetFrame>
    )
  }

  const limit = ctx.size === 'large' ? 4 : 2
  return (
    <WidgetFrame title="Notifications">
      <WidgetTitle
        right={n.unread > 0
          ? <span className="mono vwidget-tone-accent" aria-label={`${n.unread} unread`}>{n.unread}</span>
          : undefined}
      >
        Notifications
      </WidgetTitle>
      {n.recent.length === 0 ? (
        <WidgetEmpty>Nothing needs you.</WidgetEmpty>
      ) : (
        <div className="vwidget-scroll flex flex-col gap-1.5">
          {n.recent.slice(0, limit).map((item) => (
            <div key={item.id} className="flex items-start gap-2">
              <span
                className="mt-[5px] w-1.5 h-1.5 rounded-full shrink-0"
                style={{ background: item.read ? 'var(--border-emphasis)' : 'var(--accent)' }}
                aria-hidden="true"
              />
              <span className="min-w-0">
                <span className="block truncate"><WidgetLabel tone="secondary">{item.title}</WidgetLabel></span>
                {item.body && (
                  <span className="block truncate"><WidgetLabel tone="faint">{item.body}</WidgetLabel></span>
                )}
              </span>
            </div>
          ))}
        </div>
      )}
    </WidgetFrame>
  )
}

registerWidget(defineWidget({
  manifest: {
    id: 'vulos.notifications',
    name: 'Notifications',
    description: 'The most recent things the box wanted to tell you.',
    version: '1.0.0',
    author: 'Vulos',
    sizes: ['medium', 'large'],
    tick: 'none',
    permissions: ['notifications'],
  },
  render: (ctx) => <NotificationsWidget {...ctx} />,
}))
