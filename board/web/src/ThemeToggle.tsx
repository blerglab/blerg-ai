import { bindThemeAccount } from '../../../packages/settings/theme'
import { ThemePicker } from '../../../packages/settings/ThemePicker'
import type { Theme } from '../../../packages/settings/theme'
import { getAccessToken } from './authClient'
import { api } from './api'
const read = () => {
  bindThemeAccount(getAccessToken())
  if (!getAccessToken()) return Promise.reject(new Error('Sign in required'))
  return api('/api/me/theme')
}
const save = async (theme: Theme) => {
  bindThemeAccount(getAccessToken())
  if (!getAccessToken()) return
  await api('/api/me/theme', {method: 'PUT', json: {theme}})
}
export default function ThemeToggle() { return <span className="board-theme-picker"><ThemePicker read={read} save={save} /></span> }
