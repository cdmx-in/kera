import { create } from 'zustand'
import { storageGet, storageSet } from '@/lib/format'

interface UIStore {
  sidebarOpen: boolean
  settingsOpen: boolean
  settingsTab: string
  searchOpen: boolean
  showCompaction: boolean
  setSidebar(v: boolean): void
  openSettings(tab?: string): void
  closeSettings(): void
  setSearch(v: boolean): void
  setShowCompaction(v: boolean): void
}

const wide = () => typeof window !== 'undefined' && window.matchMedia?.('(min-width: 768px)').matches

export const useUI = create<UIStore>((set) => ({
  sidebarOpen: wide() && storageGet('local', 'kera:sidebar') !== '0',
  settingsOpen: false,
  settingsTab: 'general',
  searchOpen: false,
  showCompaction: storageGet('local', 'kera:show-compaction') === '1',
  setSidebar: (v) => {
    if (wide()) storageSet('local', 'kera:sidebar', v ? '1' : '0')
    set({ sidebarOpen: v })
  },
  openSettings: (tab = 'general') => set({ settingsOpen: true, settingsTab: tab }),
  closeSettings: () => set({ settingsOpen: false }),
  setSearch: (v) => set({ searchOpen: v }),
  setShowCompaction: (v) => {
    storageSet('local', 'kera:show-compaction', v ? '1' : null)
    set({ showCompaction: v })
  },
}))
