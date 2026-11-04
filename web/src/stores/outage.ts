import { create } from 'zustand'

// Statuses that mean "the server is not answering", not "the request was wrong":
// proxy errors (502/503/504) and Cloudflare's origin errors (520-530).
export const isOutageStatus = (s: number) => s === 502 || s === 503 || s === 504 || (s >= 520 && s <= 530)

interface OutageStore {
  down: boolean
  setDown(v: boolean): void
}
export const useOutage = create<OutageStore>((set) => ({ down: false, setDown: (down) => set({ down }) }))
export const reportOutage = () => useOutage.getState().setDown(true)
