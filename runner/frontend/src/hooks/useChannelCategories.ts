import { useEffect, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
export type ChannelCategory = { id: string; name: string; sessions: string[] }
export type CategoryAction = { action: string; id?: string; name?: string; session?: string; before?: string }
export const categoryDragType = 'application/x-blerg-channel-category'
export const channelDragType = 'application/x-blerg-channel'
export function useChannelCategories(root: string) {
  const [saved, setSaved] = useState<{ root: string; categories: ChannelCategory[] } | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const generation = useRef(0)
  const writing = useRef(false)
  useEffect(() => {
    let active = true
    const load = async () => {
      if (writing.current) return
      const version = generation.current
      try {
        const response = await apiFetch(`/api/sessions/${encodeURIComponent(root)}/categories`)
        if (response.ok) {
          const data = await response.json()
          if (active && version === generation.current && Array.isArray(data.categories)) setSaved({ root, categories: data.categories })
        }
      } catch { /* Keep the last saved organization while offline. */ }
    }
    void load()
    const timer = setInterval(() => { if (!document.hidden) void load() }, 10000)
    window.addEventListener('focus', load)
    return () => { active = false; clearInterval(timer); window.removeEventListener('focus', load) }
  }, [root])
  const currentRoot = useRef(root)
  useEffect(() => { currentRoot.current = root }, [root])
  const action = async (request: CategoryAction) => {
    if (writing.current) return false
    writing.current = true
    generation.current++
    setBusy(true); setError('')
    try {
      const response = await apiFetch(`/api/sessions/${encodeURIComponent(root)}/categories`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(request) })
      if (response.status === 404) {
        const refreshed = await apiFetch(`/api/sessions/${encodeURIComponent(root)}/categories`)
        if (refreshed.ok) {
          const data = await refreshed.json()
          if (currentRoot.current === root) {
            setSaved({ root, categories: data.categories })
            setError('Categories changed. Review the refreshed list and try again.')
          }
        } else if (currentRoot.current === root) {
          if (refreshed.status === 404) setSaved(null)
          setError(refreshed.status === 404 ? 'This workspace is no longer available.' : 'Categories changed. Could not refresh them. Try again.')
        }
        return false
      }
      if (!response.ok) throw new Error('Could not save categories. Try again.')
      const data = await response.json()
      if (currentRoot.current === root) setSaved({ root, categories: data.categories })
      return true
    } catch {
      if (currentRoot.current === root) setError('Could not save categories. Try again.')
      return false
    } finally { generation.current++; writing.current = false; setBusy(false) }
  }
  return { categories: saved?.root === root ? saved.categories : [], ready: saved?.root === root, action, error, busy }
}
