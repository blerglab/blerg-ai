import { describe, it, expect, beforeEach } from 'vitest'
import { useToastStore } from './useToastStore'

describe('useToastStore', () => {
  beforeEach(() => useToastStore.setState({ toasts: [] }))

  it('adds a toast with a unique id and returns it', () => {
    const id1 = useToastStore.getState().addToast({ title: 'A', body: 'a' })
    const id2 = useToastStore.getState().addToast({ title: 'B', body: 'b' })
    expect(id1).not.toBe(id2)
    expect(useToastStore.getState().toasts).toHaveLength(2)
  })

  it('removes a toast by id', () => {
    const id = useToastStore.getState().addToast({ title: 'A', body: 'a' })
    useToastStore.getState().removeToast(id)
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('variant field is preserved on the toast', () => {
    const id = useToastStore.getState().addToast({ title: 'A', body: 'a', variant: 'message' })
    const toast = useToastStore.getState().toasts.find(t => t.id === id)
    expect(toast?.variant).toBe('message')
  })

  it('variant is undefined when not provided', () => {
    const id = useToastStore.getState().addToast({ title: 'A', body: 'a' })
    const toast = useToastStore.getState().toasts.find(t => t.id === id)
    expect(toast?.variant).toBeUndefined()
  })
})
