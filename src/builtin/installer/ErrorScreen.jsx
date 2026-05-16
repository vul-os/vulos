import { useState } from 'react'

export default function ErrorScreen({ error, disk, onRetry, onBack }) {
  const [expanded, setExpanded] = useState(false)

  const isPartialWrite = error?.toLowerCase().includes('partial') ||
    error?.toLowerCase().includes('copy') ||
    error?.toLowerCase().includes('write')

  return (
    <div className="flex-1 flex flex-col items-center justify-center gap-7 p-8 text-center">
      {/* Error icon */}
      <div className="w-20 h-20 rounded-full bg-red-950/40 border border-red-800/30
        flex items-center justify-center">
        <svg width="32" height="32" viewBox="0 0 32 32" fill="none">
          <path
            d="M16 10 L16 18"
            stroke="#ef4444" strokeWidth="2.5" strokeLinecap="round"
          />
          <circle cx="16" cy="23" r="1.5" fill="#ef4444" />
        </svg>
      </div>

      <div>
        <h2 className="text-xl font-bold text-neutral-100">
          Installation failed
        </h2>
        <p className="mt-2 text-sm text-neutral-500 max-w-sm">
          An error occurred during installation. Your original disk contents
          may be partially overwritten.
        </p>
      </div>

      {/* Error detail */}
      {error && (
        <div className="w-full max-w-md rounded-lg border border-red-900/30 bg-red-950/10">
          <button
            onClick={() => setExpanded(v => !v)}
            className="w-full flex items-center justify-between px-4 py-3 text-left"
          >
            <span className="text-xs font-medium text-red-400">Error details</span>
            <span className="text-neutral-600 text-xs">{expanded ? '▲' : '▼'}</span>
          </button>
          {expanded && (
            <div className="px-4 pb-4 text-[11px] font-mono text-red-400/80 text-left
              whitespace-pre-wrap break-words border-t border-red-900/20 pt-3">
              {error}
            </div>
          )}
        </div>
      )}

      {/* Recovery guidance */}
      <div className="w-full max-w-md rounded-lg border border-neutral-800/40 bg-neutral-900/20 p-4 text-left space-y-3">
        <p className="text-xs font-medium text-neutral-300">What you can do:</p>
        <ul className="space-y-2">
          <RecoveryStep n={1}>
            Check that {disk?.path ? (
              <span className="font-mono text-neutral-300">{disk.path}</span>
            ) : 'the disk'} is properly connected and not failing.
          </RecoveryStep>
          {isPartialWrite && (
            <RecoveryStep n={2} warn>
              The disk may have partial data. Use the terminal to run{' '}
              <span className="font-mono text-amber-400">fsck</span> or
              reformat before retrying.
            </RecoveryStep>
          )}
          <RecoveryStep n={isPartialWrite ? 3 : 2}>
            Retry the installation. Transient errors (USB timeouts, memory
            pressure) often resolve on a second attempt.
          </RecoveryStep>
          <RecoveryStep n={isPartialWrite ? 4 : 3}>
            Open a terminal and check{' '}
            <span className="font-mono text-neutral-400">/var/log/vulos-installer.log</span>{' '}
            for details.
          </RecoveryStep>
        </ul>
      </div>

      {/* Actions */}
      <div className="flex gap-3 w-full max-w-xs">
        <button
          onClick={onBack}
          className="flex-1 py-2.5 text-sm text-neutral-400 hover:text-white
            border border-neutral-700 hover:border-neutral-600 rounded-lg transition-colors"
        >
          Back
        </button>
        {onRetry && (
          <button
            onClick={onRetry}
            className="flex-1 py-2.5 text-sm font-semibold bg-indigo-600
              hover:bg-indigo-500 text-white rounded-lg transition-colors"
          >
            Retry
          </button>
        )}
      </div>
    </div>
  )
}

function RecoveryStep({ n, children, warn }) {
  return (
    <li className="flex items-start gap-2.5">
      <span className={`shrink-0 w-4 h-4 rounded-full flex items-center justify-center text-[9px] font-bold mt-0.5
        ${warn ? 'bg-amber-900/50 text-amber-400' : 'bg-neutral-800 text-neutral-400'}`}>
        {n}
      </span>
      <span className={`text-xs leading-relaxed ${warn ? 'text-amber-400/80' : 'text-neutral-500'}`}>
        {children}
      </span>
    </li>
  )
}
