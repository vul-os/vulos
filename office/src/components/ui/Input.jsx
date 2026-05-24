/**
 * Input — text input matched to Button heights, with optional leading slot
 * and an unobtrusive label/hint pattern.
 *
 * Usage:
 *   <Input label="Name" hint="As shown to recipients" />
 *   <Input leading={<Search size={14}/>} placeholder="Search…" />
 *
 * Errors: pass `error="message"` to show in danger colour.
 */

import { forwardRef, useId } from 'react'

const sizeClasses = {
  sm: 'h-7 text-xs',
  md: 'h-9 text-sm',
  lg: 'h-11 text-md',
}

const Input = forwardRef(function Input(
  {
    label,
    hint,
    error,
    size = 'md',
    leading,
    trailing,
    className = '',
    id,
    ...rest
  },
  ref,
) {
  const reactId = useId()
  const inputId = id || reactId

  const wrapperCn = [
    'group flex items-center w-full bg-paper border border-line rounded-md',
    'transition-[border-color,box-shadow] duration-fast ease-out',
    'focus-within:border-accent focus-within:shadow-focus',
    error ? 'border-danger focus-within:border-danger' : '',
    sizeClasses[size] || sizeClasses.md,
  ]
    .filter(Boolean)
    .join(' ')

  return (
    <div className={className}>
      {label && (
        <label htmlFor={inputId} className="block text-xs text-ink-muted font-medium mb-1.5 tracking-tightish">
          {label}
        </label>
      )}
      <div className={wrapperCn}>
        {leading && <span className="pl-2.5 text-ink-faint flex items-center">{leading}</span>}
        <input
          ref={ref}
          id={inputId}
          className={[
            'flex-1 bg-transparent outline-none',
            'px-3',
            leading ? 'pl-2' : '',
            trailing ? 'pr-2' : '',
            'placeholder:text-ink-faint',
            'text-ink',
          ].join(' ')}
          {...rest}
        />
        {trailing && <span className="pr-2.5 text-ink-faint flex items-center">{trailing}</span>}
      </div>
      {(hint || error) && (
        <p className={`mt-1.5 text-2xs ${error ? 'text-danger' : 'text-ink-faint'}`}>
          {error || hint}
        </p>
      )}
    </div>
  )
})

export default Input
