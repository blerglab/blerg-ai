import { setPersonTimeZone } from './friendlyTime'
import { apiFetch } from '../apiFetch'

// Capture the browser default on sign-in, before Account Settings has ever been opened.
// Conditional insertion preserves the person's saved choice across browsers and tabs.
export async function initializePersonTime(): Promise<void> {
  setPersonTimeZone()
  try {
    const response = await apiFetch('/api/me/preferences')
    if (!response.ok) return
    const preferences = await response.json() as { person_time?: { timezone?: string } }
    if (preferences.person_time?.timezone) {
      setPersonTimeZone(preferences.person_time.timezone)
      return
    }
    await apiFetch('/api/me/preferences', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'If-None-Match': '*' },
      body: JSON.stringify({ person_time: { timezone: Intl.DateTimeFormat().resolvedOptions().timeZone } }),
    })
  } catch { /* Retry on the next page load; never guess the person's zone on the server. */ }
}
