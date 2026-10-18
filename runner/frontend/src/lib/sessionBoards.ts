import { useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'

interface SessionBoard { id: string; name: string }
interface BoardList { configured: boolean; boards: SessionBoard[] }
type BoardState = ({ state: 'ready' } & BoardList) | { state: 'loading' | 'failed'; configured: false; boards: SessionBoard[] }

// Board access is checked live on every open; a remembered choice never grants access.
export function useSessionBoards(enabled: boolean) {
  const [attempt, setAttempt] = useState(0)
  const [result, setResult] = useState<BoardState>({ state: 'loading', configured: false, boards: [] })
  useEffect(() => {
    if (!enabled) return
    let active = true
    const controller = new AbortController()
    // A stalled discovery request must not stall launches without a board.
    const timer = setTimeout(() => {
      controller.abort()
      if (active) setResult({ state: 'failed', configured: false, boards: [] })
      active = false
    }, 8000)
    void apiFetch('/api/session-boards', { signal: controller.signal }).then(async response => {
      if (!response.ok) throw new Error('Boards unavailable')
      const value: unknown = await response.json()
      if (!value || typeof value !== 'object' || !('configured' in value) || typeof value.configured !== 'boolean' || !('boards' in value) || !Array.isArray(value.boards) || !value.boards.every(b => b && typeof b.id === 'string' && typeof b.name === 'string')) throw new Error('Invalid boards response')
      if (active) setResult({ state: 'ready', configured: value.configured, boards: value.boards })
    }).catch(() => {
      if (active) setResult({ state: 'failed', configured: false, boards: [] })
    }).finally(() => clearTimeout(timer))
    return () => { active = false; clearTimeout(timer); controller.abort() }
  }, [enabled, attempt])
  return { ...result, retry: () => setAttempt(a => a + 1) }
}
