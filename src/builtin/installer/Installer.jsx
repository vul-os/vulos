/**
 * Installer.jsx — Vula OS bare-metal installer (live-USB)
 *
 * Standalone component: no AppRegistry registration, no Shell imports.
 * Wire it up from outside when the live-USB gate is detected.
 *
 * Flow:
 *   welcome → disk-selection → progress (WS) → success/reboot
 *                                            → error/recovery
 *
 * Backend API contract (BMINIT-12):
 *   GET  /api/installer/status   → { mode, installed, ram_gb, … }
 *   GET  /api/installer/disks    → { disks: [Disk, …] }
 *   POST /api/installer/install  → { disk, confirm: true }
 *   WS   /api/installer/progress → { progress, phase, log, done, error }
 *   POST /api/installer/reboot   → 200 OK
 */

import { useState, useCallback } from 'react'
import WelcomeScreen from './WelcomeScreen'
import DiskSelection from './DiskSelection'
import ProgressScreen from './ProgressScreen'
import SuccessScreen from './SuccessScreen'
import ErrorScreen from './ErrorScreen'

// STEP constants for the wizard
const STEP = {
  WELCOME: 'welcome',
  DISK: 'disk',
  PROGRESS: 'progress',
  SUCCESS: 'success',
  ERROR: 'error',
}

function StepDots({ step }) {
  const steps = [STEP.WELCOME, STEP.DISK, STEP.PROGRESS, STEP.SUCCESS]
  const current = steps.indexOf(step)

  return (
    <div className="flex items-center gap-1.5 py-3 justify-center">
      {steps.map((s, i) => (
        <span
          key={s}
          className={`rounded-full transition-all
            ${i === current
              ? 'w-4 h-1.5 bg-indigo-500'
              : i < current
                ? 'w-1.5 h-1.5 bg-indigo-700'
                : 'w-1.5 h-1.5 bg-neutral-800'
            }`}
        />
      ))}
    </div>
  )
}

export default function Installer({ onClose }) {
  const [step, setStep] = useState(STEP.WELCOME)
  const [selectedDisk, setSelectedDisk] = useState(null)
  const [installError, setInstallError] = useState(null)

  const handleDiskSelected = useCallback((disk) => {
    setSelectedDisk(disk)
    setStep(STEP.PROGRESS)
  }, [])

  const handleInstallSuccess = useCallback(() => {
    setStep(STEP.SUCCESS)
  }, [])

  const handleInstallError = useCallback((errMsg) => {
    setInstallError(errMsg)
    setStep(STEP.ERROR)
  }, [])

  const handleRetry = useCallback(() => {
    setInstallError(null)
    if (selectedDisk) {
      // Go back to disk selection so the user can confirm again
      setStep(STEP.DISK)
    } else {
      setStep(STEP.WELCOME)
    }
  }, [selectedDisk])

  const handleReboot = useCallback(() => {
    // Backend handles the actual reboot; just show a waiting state
    // (SuccessScreen handles the POST)
  }, [])

  return (
    <div className="h-full flex flex-col bg-neutral-950 text-neutral-200 overflow-hidden select-none">
      {/* Title bar */}
      <div className="shrink-0 flex items-center justify-between px-4 pt-4 pb-2">
        <div className="flex items-center gap-2">
          <InstallerIcon />
          <span className="text-xs font-semibold text-neutral-400 tracking-wide uppercase">
            Vula OS Installer
          </span>
        </div>
        {onClose && (
          <button
            onClick={onClose}
            className="w-6 h-6 rounded-full flex items-center justify-center
              text-neutral-600 hover:text-neutral-400 hover:bg-neutral-800 transition-colors"
            aria-label="Close installer"
          >
            <svg width="10" height="10" viewBox="0 0 10 10">
              <path d="M1 1 L9 9 M9 1 L1 9" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
            </svg>
          </button>
        )}
      </div>

      {/* Step dots */}
      {step !== STEP.ERROR && <StepDots step={step} />}

      {/* Content area */}
      <div className="flex-1 flex flex-col min-h-0 overflow-hidden">
        {step === STEP.WELCOME && (
          <WelcomeScreen
            onStart={() => setStep(STEP.DISK)}
          />
        )}

        {step === STEP.DISK && (
          <DiskSelection
            onSelect={handleDiskSelected}
            onBack={() => setStep(STEP.WELCOME)}
          />
        )}

        {step === STEP.PROGRESS && selectedDisk && (
          <ProgressScreen
            disk={selectedDisk}
            onSuccess={handleInstallSuccess}
            onError={handleInstallError}
          />
        )}

        {step === STEP.SUCCESS && (
          <SuccessScreen
            disk={selectedDisk}
            onReboot={handleReboot}
            onClose={onClose}
          />
        )}

        {step === STEP.ERROR && (
          <ErrorScreen
            error={installError}
            disk={selectedDisk}
            onRetry={handleRetry}
            onBack={() => setStep(STEP.DISK)}
          />
        )}
      </div>
    </div>
  )
}

function InstallerIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 18 18" fill="none">
      <rect x="1" y="1" width="16" height="16" rx="4" fill="#1a1a2e" stroke="#6366f1" strokeWidth="1" />
      <path d="M5 9 L9 13 L13 5" stroke="#6366f1" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}
