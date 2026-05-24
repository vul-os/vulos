import { create } from 'zustand'
import { api } from '../lib/api'
import { writeDraft, clearDraft } from '../lib/draftStore'

function defaultContent(type) {
  switch (type) {
    case 'doc':
      return { type: 'doc', content: [{ type: 'paragraph' }] }
    case 'sheet':
      return [{ name: 'Sheet1', celldata: [], config: {} }]
    case 'slide':
      return { theme: 'black', transition: 'slide', slides: [{ id: crypto.randomUUID(), title: '', content: '<p></p>', notes: '' }] }
    default:
      return null
  }
}

// Per-file save state: 'idle' | 'dirty' | 'saving' | 'saved' | 'error'
// Stored as a plain map outside Zustand to avoid excessive re-renders in editors
// that only care about their own file's state.
const saveStateListeners = new Map() // id -> Set<fn>
const saveStates = new Map()         // id -> { status, error }

export function getSaveState(id) {
  return saveStates.get(id) || { status: 'idle', error: null }
}

export function onSaveStateChange(id, fn) {
  if (!saveStateListeners.has(id)) saveStateListeners.set(id, new Set())
  saveStateListeners.get(id).add(fn)
  return () => saveStateListeners.get(id).delete(fn)
}

function setSaveState(id, status, error = null) {
  saveStates.set(id, { status, error })
  const listeners = saveStateListeners.get(id)
  if (listeners) listeners.forEach((fn) => fn({ status, error }))
}

export const useFilesStore = create((set, get) => ({
  files: [],
  loading: false,

  fetchFiles: async () => {
    set({ loading: true })
    try {
      const files = await api.listFiles()
      set({ files, loading: false })
    } catch {
      set({ loading: false })
    }
  },

  createFile: async (name, type) => {
    const file = await api.createFile(name, type, defaultContent(type))
    set({ files: [file, ...get().files] })
    return file
  },

  updateFile: async (id, name, content) => {
    const file = await api.updateFile(id, name, content)
    set({ files: get().files.map((f) => (f.id === id ? file : f)) })
    return file
  },

  deleteFile: async (id) => {
    await api.deleteFile(id)
    set({ files: get().files.filter((f) => f.id !== id) })
  },

  renameFile: async (id, name) => {
    const file = get().files.find((f) => f.id === id)
    if (file) await get().updateFile(id, name, file.content)
  },

  /**
   * Crash-safe save:
   *  1. Mark dirty in save state.
   *  2. Persist draft to IndexedDB BEFORE the network write.
   *  3. Attempt network write; on success clear draft + mark saved.
   *  4. On failure keep draft, mark error, and throw so callers can retry.
   */
  saveFileWithDraft: async (id, name, content) => {
    setSaveState(id, 'saving')
    // Write draft first — survives a tab-close mid-flight
    await writeDraft(id, name, content)
    try {
      const file = await api.updateFile(id, name, content)
      set({ files: get().files.map((f) => (f.id === id ? file : f)) })
      await clearDraft(id)
      setSaveState(id, 'saved')
      return file
    } catch (err) {
      // Draft survives in IndexedDB; surface error state
      setSaveState(id, 'error', err.message || 'Save failed')
      throw err
    }
  },

  markDirty: (id) => {
    const current = getSaveState(id)
    if (current.status !== 'saving') setSaveState(id, 'dirty')
  },
}))
