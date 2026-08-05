import { searchApps, type App } from './AppRegistry'

export interface EmptyIntent {
  type: 'empty'
}

export interface SystemIntent {
  type: 'system'
  action: 'open_settings' | 'open_files'
  label: string
  section?: string
}

export interface CommandIntent {
  type: 'command'
  value: string
}

export interface LaunchServiceIntent {
  type: 'launch_service'
  service: App
}

export interface ServiceSuggestionsIntent {
  type: 'service_suggestions'
  matches: App[]
}

export interface MathIntent {
  type: 'math'
  value: string
}

export interface MissionIntent {
  type: 'mission'
  value: string
}

export type Intent =
  | EmptyIntent
  | SystemIntent
  | CommandIntent
  | LaunchServiceIntent
  | ServiceSuggestionsIntent
  | MathIntent
  | MissionIntent

type SystemCommand = Omit<SystemIntent, 'type'>

export function classifyIntent(input: string): Intent {
  const trimmed = input.trim()
  if (!trimmed) return { type: 'empty' }

  // System command: /settings, /wifi, /backup etc. These map to real in-shell
  // surfaces — Settings panels (opened to a specific section) or a builtin app —
  // NOT legacy pseudo-hosts. `section` selects the Settings sidebar entry; the
  // `files` command opens the File Explorer builtin.
  if (trimmed.startsWith('/')) {
    const cmd = trimmed.slice(1).toLowerCase().trim()
    const sys: Record<string, SystemCommand> = {
      settings: { action: 'open_settings', label: 'Settings', section: 'ai' },
      persona: { action: 'open_settings', label: 'Settings', section: 'ai' },
      wifi: { action: 'open_settings', label: 'WiFi', section: 'wifi' },
      network: { action: 'open_settings', label: 'Remote Access', section: 'network' },
      backup: { action: 'open_settings', label: 'Backup & Sync', section: 'vault' },
      vault: { action: 'open_settings', label: 'Backup & Sync', section: 'vault' },
      files: { action: 'open_files', label: 'File Explorer' },
    }
    if (sys[cmd]) return { type: 'system', ...sys[cmd] }
    return { type: 'command', value: cmd }
  }

  // App match
  const matches = searchApps(trimmed)
  if (matches.length === 1) return { type: 'launch_service', service: matches[0] }
  if (matches.length > 1 && matches.length <= 3) return { type: 'service_suggestions', matches }

  // Inline math
  if (/[\d]+\s*[+\-*/÷×%]\s*[\d]/.test(trimmed) || /\d+%\s*(of|on|tax)/i.test(trimmed)) {
    return { type: 'math', value: trimmed }
  }

  // Everything else is a mission for the AI
  return { type: 'mission', value: trimmed }
}
