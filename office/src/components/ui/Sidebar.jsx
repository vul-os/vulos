/**
 * Sidebar primitives — a vertical app rail with a quiet, restrained look.
 *
 * Pieces:
 *   - <Sidebar>             root container; manages width (collapsed/expanded)
 *   - <Sidebar.Brand>       logo + product name (fades wordmark when collapsed)
 *   - <Sidebar.Section>     labelled group; auto-hides label when collapsed
 *   - <Sidebar.Item>        nav row; uses an accent left-rail when active
 *   - <Sidebar.Footer>      sticky bottom region (settings, collapse toggle…)
 *
 * Behaviour notes:
 *   - The active state uses a 2px accent rail on the LEFT — a Mercury / Linear
 *     trait. We avoid colouring the whole row background, which would shout.
 *   - Width transitions are 200ms ease-out so collapsing feels considered.
 */

import { useState, createContext, useContext } from 'react'
import { NavLink } from 'react-router-dom'

const SidebarCtx = createContext({ collapsed: false })

function Sidebar({ collapsed, children, className = '' }) {
  return (
    <SidebarCtx.Provider value={{ collapsed }}>
      <aside
        className={[
          'relative flex flex-col flex-shrink-0',
          'bg-bg-elev2 text-ink-muted border-r border-line',
          'transition-[width] duration-base ease-out',
          collapsed ? 'w-14' : 'w-60',
          className,
        ].join(' ')}
      >
        {children}
      </aside>
    </SidebarCtx.Provider>
  )
}

Sidebar.Brand = function SidebarBrand({ logoSrc, name = 'Vulos Office' }) {
  const { collapsed } = useContext(SidebarCtx)
  return (
    <div className="flex items-center gap-2.5 px-3 h-12 border-b border-line">
      {logoSrc ? (
        <img
          src={logoSrc}
          alt={name}
          className="w-7 h-7 rounded-md object-cover flex-shrink-0"
        />
      ) : (
        <div className="w-7 h-7 rounded-md bg-accent text-white flex items-center justify-center text-xs font-semibold">
          V
        </div>
      )}
      {!collapsed && (
        <span className="text-sm font-semibold tracking-tightish text-ink truncate">
          {name}
        </span>
      )}
    </div>
  )
}

Sidebar.Section = function SidebarSection({ label, children, className = '' }) {
  const { collapsed } = useContext(SidebarCtx)
  return (
    <div className={`px-1.5 py-2 ${className}`}>
      {!collapsed && label && (
        <p className="px-2 pt-1 pb-1 text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow">
          {label}
        </p>
      )}
      {collapsed && label && <div className="border-t border-line mx-2 my-1.5" />}
      <div className="flex flex-col gap-0.5">{children}</div>
    </div>
  )
}

/**
 * Sidebar.Item — accepts either `to` (renders NavLink) or `onClick` (button).
 * Active styling uses an accent left-rail (2px) instead of filling the row.
 */
Sidebar.Item = function SidebarItem({
  to,
  end,
  onClick,
  icon: Icon,
  iconAccent,    // optional brand colour for the icon when not active
  title,
  children,
  variant = 'nav',
}) {
  const { collapsed } = useContext(SidebarCtx)

  const renderInner = (isActive) => (
    <>
      <span
        aria-hidden
        className={[
          'absolute left-0 top-1 bottom-1 w-[2px] rounded-r-full',
          'transition-colors duration-fast ease-out',
          isActive ? 'bg-accent' : 'bg-transparent',
        ].join(' ')}
      />
      {Icon && (
        <Icon
          size={15}
          className={[
            'flex-shrink-0 transition-colors duration-fast ease-out',
            isActive ? 'text-ink' : iconAccent || 'text-ink-faint',
          ].join(' ')}
        />
      )}
      {!collapsed && (
        <span className="truncate text-sm tracking-tightish">{children}</span>
      )}
    </>
  )

  const cn = (isActive) =>
    [
      'relative flex items-center gap-2.5 h-8 px-3 rounded-md',
      'transition-colors duration-fast ease-out',
      collapsed ? 'justify-center' : '',
      isActive
        ? 'bg-paper text-ink shadow-e1'
        : 'text-ink-muted hover:bg-accent-tint hover:text-ink',
      variant === 'danger' ? 'hover:bg-danger-bg hover:text-danger' : '',
    ].join(' ')

  if (to) {
    return (
      <NavLink to={to} end={end} title={title} className={({ isActive }) => cn(isActive)}>
        {({ isActive }) => renderInner(isActive)}
      </NavLink>
    )
  }
  return (
    <button type="button" onClick={onClick} title={title} className={cn(false)}>
      {renderInner(false)}
    </button>
  )
}

Sidebar.Footer = function SidebarFooter({ children }) {
  return (
    <div className="mt-auto border-t border-line py-2 px-1.5 flex flex-col gap-0.5">
      {children}
    </div>
  )
}

export { Sidebar }
export default Sidebar
