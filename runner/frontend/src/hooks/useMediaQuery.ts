import { useCallback, useSyncExternalStore } from 'react'

/** useMediaQuery follows a CSS media query: true while it matches, re-rendering when that changes. */
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback((onChange: () => void) => {
    const mql = window.matchMedia(query)
    mql.addEventListener('change', onChange)
    return () => mql.removeEventListener('change', onChange)
  }, [query])
  return useSyncExternalStore(subscribe, () => window.matchMedia(query).matches)
}
