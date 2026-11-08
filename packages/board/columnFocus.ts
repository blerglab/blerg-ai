import { useEffect, useState } from 'react'

/** A host supplies a tab-specific scope; Board uses its single board view. */
export function useColumnFocus(scope: string, enabled = true, available?: string[]) {
  const key = `blerg-board:focus:${scope}`
  const read = () => { try { return localStorage.getItem(key) ?? '' } catch { return '' } }
  const [state, setState] = useState(() => ({ key, column: read() }))
  if (state.key !== key) setState({ key, column: read() })
  const column = state.key === key ? state.column : read()
  const valid = available === undefined || !column || available.includes(column)
  useEffect(() => { if (!valid) setState({ key, column: '' }) }, [valid, key])
  useEffect(() => { try { localStorage.setItem(key, column) } catch { /* Storage is optional. */ } }, [key, column])
  useEffect(() => {
    if (!enabled || !column) return
    const escape = (event: KeyboardEvent) => {
      const modal = [...document.querySelectorAll('[role="dialog"], [role="menu"]')].some(element => !element.closest('[hidden], [aria-hidden="true"]'))
      if (event.key === 'Escape' && !event.defaultPrevented && !modal) setState({ key, column: '' })
    }
    document.addEventListener('keydown', escape)
    return () => document.removeEventListener('keydown', escape)
  }, [column, enabled, key])
  return { column: valid ? column : '', toggle: (id: string) => setState(current => ({ key, column: current.column === id ? '' : id })) }
}
