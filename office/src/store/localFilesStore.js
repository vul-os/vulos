import { create } from 'zustand'
import { api } from '../lib/api'

export const useLocalFilesStore = create((set, get) => ({
  files: [],
  loading: false,
  scanned: false,
  error: null,

  scan: async () => {
    if (get().loading) return
    set({ loading: true, error: null })
    try {
      const files = await api.scanLocalFiles()
      set({ files, loading: false, scanned: true })
    } catch (e) {
      set({ loading: false, error: e.message, scanned: true })
    }
  },
}))
