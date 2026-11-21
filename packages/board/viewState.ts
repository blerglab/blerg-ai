import { useEffect, useState, type Dispatch, type SetStateAction } from 'react'

export function useBoardViewState<T>(scope: string, field: string, initial: T): [T, Dispatch<SetStateAction<T>>] {
  const key = `blerg-board:view:${scope}:${field}`
  const [value, setValue] = useState<T>(() => {
    try { const saved = localStorage.getItem(key); return saved === null ? initial : JSON.parse(saved) as T } catch { return initial }
  })
  useEffect(() => { try { localStorage.setItem(key, JSON.stringify(value)) } catch { /* Optional storage. */ } }, [key, value])
  return [value, setValue]
}
