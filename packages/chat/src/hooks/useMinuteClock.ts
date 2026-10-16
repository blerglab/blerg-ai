import { useEffect, useState } from 'react'

/** The client clock (Date.now()), read when the component mounts and again each minute: slow
 *  enough that a view built on it does not redraw for it, fast enough for a threshold counted in
 *  hours. */
export function useMinuteClock(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(t)
  }, [])
  return now
}
