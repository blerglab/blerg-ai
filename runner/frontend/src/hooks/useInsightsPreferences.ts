import { useCallback, useState, type Dispatch, type SetStateAction } from 'react'
import { signedInAccount } from '../lib/liveBoardCard'

// Like the Board's local filters, choices belong to the person, not a workspace.
export function useInsightsPreference<T>(name: string, fallback: T): [T, Dispatch<SetStateAction<T>>] {
  const account = signedInAccount()
  const key = `blerg-runner.insights.${encodeURIComponent(account)}.${name}`
  const read = () => {
    try {
      const stored = account ? JSON.parse(localStorage.getItem(key) ?? 'null') : null
      return stored !== null && typeof stored === typeof fallback && !Array.isArray(stored) ? stored as T : fallback
    } catch { return fallback }
  }
  const [state, setState] = useState(() => ({ key, value: read() }))
  let value = state.value
  if (state.key !== key) { value = read(); setState({ key, value }) }
  const update = useCallback<Dispatch<SetStateAction<T>>>(next => setState(before => {
    const result = typeof next === 'function' ? (next as (value: T) => T)(before.value) : next
    if (account) { try { localStorage.setItem(key, JSON.stringify(result)) } catch { /* private storage */ } }
    return { key, value: result }
  }), [account, key])
  return [value, update]
}
