import { bindThemeAccount } from '../../../packages/settings/theme'
import { ThemePicker } from '../../../packages/settings/ThemePicker'
import type { Theme } from '../../../packages/settings/theme'
import { getAccessToken } from './authClient'
import { apiFetch } from './apiFetch'
const read = async () => {
  bindThemeAccount(getAccessToken())
  if (!getAccessToken()) throw new Error('Sign in required')
  const response = await apiFetch('/api/me/theme')
  if (!response.ok) throw new Error('Theme unavailable')
  return response.json() as Promise<unknown>
}
const save = async (theme: Theme) => {
  bindThemeAccount(getAccessToken())
  if (!getAccessToken()) return
  const response = await apiFetch('/api/me/theme', {
    method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({theme}),
  })
  if (!response.ok) throw new Error('Theme unavailable')
}
export default function ThemeToggle() { return <ThemePicker read={read} save={save} /> }
