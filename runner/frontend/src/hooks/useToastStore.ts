import { create } from 'zustand'

export interface Toast {
  id: number
  title: string
  body: string
  url?: string
  variant?: 'session' | 'message'
}

interface ToastStore {
  toasts: Toast[]
  addToast: (t: Omit<Toast, 'id'>) => number
  removeToast: (id: number) => void
}

let nextId = 1

export const useToastStore = create<ToastStore>((set) => ({
  toasts: [],
  addToast: (t) => {
    const id = nextId++
    set((state) => ({ toasts: [...state.toasts, { ...t, id }] }))
    return id
  },
  removeToast: (id) =>
    set((state) => ({ toasts: state.toasts.filter((x) => x.id !== id) })),
}))
