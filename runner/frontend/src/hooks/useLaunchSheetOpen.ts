import { useCallback, useState } from 'react'

// Whether the New-session sheet is open, remembered for this tab across a
// page reload (sessionStorage: per tab, gone when the tab closes).
//
// Why this can't be plain in-memory state: the access token lives 10 minutes
// and only an HTTP 401 renews it, via apiFetch sending the WHOLE PAGE through
// core's /auth/refresh and back. The sessions page is otherwise fed by a
// WebSocket that stays authenticated, so on an idle tab opening this sheet is
// very often the first HTTP call in a while: its fetches get a 401, the page
// reloads, and the sheet — which had only just appeared — came back closed
// ("it pops up for a fraction of a second and then goes away; the second
// press works" — by then the token is fresh). Remembering "open" here makes
// the reloaded page reopen the sheet instead.
const KEY = 'blerg.launch.open'

function read(): boolean {
  try {
    return sessionStorage.getItem(KEY) === '1'
  } catch {
    return false
  }
}

function write(open: boolean) {
  try {
    if (open) sessionStorage.setItem(KEY, '1')
    else sessionStorage.removeItem(KEY)
  } catch {
    // Storage unavailable: the sheet still works, it just isn't restored.
  }
}

export function useLaunchSheetOpen(): [boolean, (open: boolean) => void] {
  const [open, setOpenState] = useState(read)
  const setOpen = useCallback((next: boolean) => {
    write(next)
    setOpenState(next)
  }, [])
  return [open, setOpen]
}
