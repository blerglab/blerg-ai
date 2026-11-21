import { afterEach, expect, it, vi } from 'vitest'
import { effectiveTheme, setTheme, THEMES } from '../../../packages/settings/theme'

afterEach(() => { document.documentElement.removeAttribute('data-theme'); localStorage.clear() })
it('offers system following, the five account presets and the bundled open-source schemes, and resolves every explicit choice before the OS', () => {
 expect(THEMES.map(t => t.id)).toEqual(['system', 'dark', 'light', 'colorblind-dark', 'colorblind-light', 'high-contrast',
   'catppuccin-mocha', 'catppuccin-latte', 'dracula', 'nord', 'tokyo-night'])
 vi.stubGlobal('matchMedia', () => ({matches: true}))
 for (const {id} of THEMES) { setTheme(id); expect(effectiveTheme()).toBe(id === 'system' ? 'light' : id); expect(localStorage.getItem('blerg-theme')).toBe(id) }
})
it('follows live OS changes after clearing an explicit pin', () => {
 let light=false
 vi.stubGlobal('matchMedia', () => ({matches:light}))
 setTheme('high-contrast')
 setTheme('system')
 expect(effectiveTheme()).toBe('dark')
 light=true
 expect(effectiveTheme()).toBe('light')
 expect(document.documentElement.hasAttribute('data-theme')).toBe(false)
 vi.unstubAllGlobals()
})
