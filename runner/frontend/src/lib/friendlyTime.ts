import { dayKey } from '@blerglab/chat'

// Initialized with the account's saved person_time setting; browser local until it arrives.
let personTimeZone: string | undefined
export function setPersonTimeZone(timeZone?: string) {
  personTimeZone = timeZone
}

/** Inbox and hovercard timestamps, in the person's zone and American English. */
export function longWhen(iso: string, now = Date.now()) {
  const at = Date.parse(iso)
  if (!Number.isFinite(at)) return ''
  const options = { locale: 'en-US', timeZone: personTimeZone }
  const daysAgo = dayKey(now, options) - dayKey(at, options)
  const time = new Date(at).toLocaleTimeString('en-US', { timeZone: personTimeZone, hour: 'numeric', minute: '2-digit' })
  if (daysAgo === 0) return `today ${time}`
  if (daysAgo === 1) return `yesterday ${time}`
  const date = new Date(at).toLocaleDateString('en-US', { timeZone: personTimeZone, month: 'short', day: 'numeric' })
  return `${date}, ${time}`
}
