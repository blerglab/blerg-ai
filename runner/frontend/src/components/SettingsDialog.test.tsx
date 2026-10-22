import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { CODE_TOKEN_NAMES, DEFAULT_CODE_COLOURS, HOVERCARDS_KEY, codeThemes, hovercardsEnabled } from '@blerglab/chat'
import SettingsDialog from './SettingsDialog'
import { CODE_COLOURS_KEY, fetchCodeColours, getCodeColours, saveCodeColours, setCodeColours } from '../lib/codeColours'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('./DelegationRulesSection', () => ({ default: () => null }))
vi.mock('../apiFetch', () => ({ apiFetch }))

const preview = () => screen.getByTestId('cc-preview')
const respond = (status: number, body: unknown = {}) =>
  Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body) } as Response)

beforeEach(() => {
  localStorage.clear()
  setCodeColours(DEFAULT_CODE_COLOURS)
  apiFetch.mockReset()
  document.documentElement.setAttribute('data-theme', 'dark')
})
afterEach(() => document.documentElement.removeAttribute('data-theme'))

describe('SettingsDialog', () => {
  it('Hover previews: on by default, one checkbox turns every hovercard off and on (kept per browser)', () => {
    render(<SettingsDialog onClose={() => {}} />)
    const box = screen.getByRole('checkbox', { name: 'Hover previews' })
    expect(box).toBeChecked()
    fireEvent.click(box)
    expect(box).not.toBeChecked()
    expect(hovercardsEnabled()).toBe(false)
    expect(localStorage.getItem(HOVERCARDS_KEY)).toBe('0')
    fireEvent.click(box)
    expect(hovercardsEnabled()).toBe(true)
    expect(localStorage.getItem(HOVERCARDS_KEY)).toBeNull()
  })

  it('shows real highlighted blocks in five languages, in the chosen theme, and switches live', async () => {
    render(<SettingsDialog onClose={() => {}} />)
    await waitFor(() => expect(preview().querySelectorAll('.hljs-keyword').length).toBeGreaterThan(2))
    expect(preview().querySelectorAll('.md-codeblock').length).toBe(5)
    expect(preview().querySelector('.md-diff-add')).not.toBeNull()
    expect(preview().style.getPropertyValue('--chat-code-keyword')).toBe(codeThemes.blerg.tokens.keyword)
    fireEvent.change(screen.getByTestId('cc-theme-dark'), { target: { value: 'dracula' } })
    expect(preview().style.getPropertyValue('--chat-code-keyword')).toBe(codeThemes.dracula.tokens.keyword)
    expect(preview().style.getPropertyValue('--chat-code-bg')).toBe(codeThemes.dracula.tokens.bg)
    // The light picker previews the light mode.
    fireEvent.change(screen.getByTestId('cc-theme-light'), { target: { value: 'github-light' } })
    expect(preview().style.getPropertyValue('--chat-code-bg')).toBe('#ffffff')
  })

  it('applies a colour changed under Customize, and resets it', () => {
    render(<SettingsDialog onClose={() => {}} />)
    fireEvent.change(screen.getByTestId('cc-theme-dark'), { target: { value: 'nord' } })
    fireEvent.change(screen.getByLabelText('Keywords color'), { target: { value: '#112233' } })
    expect(preview().style.getPropertyValue(CODE_TOKEN_NAMES.keyword)).toBe('#112233')
    fireEvent.click(screen.getByRole('button', { name: 'Reset' }))
    expect(preview().style.getPropertyValue(CODE_TOKEN_NAMES.keyword)).toBe(codeThemes.nord.tokens.keyword)
  })

  it('changes nothing in the app until Save, then saves to the server and the browser', async () => {
    apiFetch.mockImplementation(() => respond(200, {}))
    const onClose = vi.fn()
    render(<SettingsDialog onClose={onClose} />)
    fireEvent.change(screen.getByTestId('cc-theme-dark'), { target: { value: 'monokai' } })
    expect(getCodeColours().dark).toBe('blerg')
    expect(document.documentElement.style.getPropertyValue('--chat-code-keyword')).not.toBe(codeThemes.monokai.tokens.keyword)
    fireEvent.click(screen.getByRole('button', { name: 'Save colors' }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    const [url, init] = apiFetch.mock.calls[0]
    expect(url).toBe('/api/me/preferences')
    expect(init.method).toBe('PUT')
    expect(JSON.parse(init.body).code_colours.dark).toBe('monokai')
    expect(JSON.parse(localStorage.getItem(CODE_COLOURS_KEY)!).dark).toBe('monokai')
    expect(document.documentElement.style.getPropertyValue('--chat-code-keyword')).toBe(codeThemes.monokai.tokens.keyword)
  })

  it('says why when the server refuses, and keeps the old choice', async () => {
    apiFetch.mockImplementation(() => respond(400, { error: 'code_colours: unknown theme' }))
    render(<SettingsDialog onClose={() => {}} />)
    fireEvent.change(screen.getByTestId('cc-theme-light'), { target: { value: 'nord' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save colors' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('unknown theme')
    expect(getCodeColours().light).toBe('blerg')
  })

  it('Save is off until something changed; Escape closes without saving', () => {
    const onClose = vi.fn()
    render(<SettingsDialog onClose={onClose} />)
    expect(screen.getByRole('button', { name: 'Save colors' })).toBeDisabled()
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
    expect(apiFetch).not.toHaveBeenCalled()
  })
})

describe('code colours persistence', () => {
  it('reads the server value, and falls back to nothing on failure', async () => {
    apiFetch.mockImplementationOnce(() => respond(200, { code_colours: { dark: 'nord', light: 'bogus', overrides: { dark: { keyword: '#abcdef' } } } }))
    expect(await fetchCodeColours()).toEqual({ dark: 'nord', light: 'blerg', overrides: { dark: { keyword: '#abcdef' }, light: {} } })
    apiFetch.mockImplementationOnce(() => respond(500))
    expect(await fetchCodeColours()).toBeNull()
    apiFetch.mockImplementationOnce(() => Promise.reject(new Error('offline')))
    expect(await saveCodeColours(DEFAULT_CODE_COLOURS)).toMatch(/reach/)
  })

  it('paints the stored choice on the page, in the mode the page shows', () => {
    setCodeColours({ dark: 'nord', light: 'github-light', overrides: { dark: {}, light: {} } })
    expect(document.documentElement.style.getPropertyValue('--chat-code-bg')).toBe(codeThemes.nord.tokens.bg)
    document.documentElement.setAttribute('data-theme', 'light')
    setCodeColours(getCodeColours())
    expect(document.documentElement.style.getPropertyValue('--chat-code-bg')).toBe('#ffffff')
  })
})

describe('SettingsDialog on a phone', () => {
  // jsdom lays nothing out: the narrow-screen rules are checked in the stylesheets themselves.
  it('is a full-screen sheet below 900px with one column of pickers and full-width buttons', async () => {
    const fsName = 'node:fs'
    const fs = (await import(/* @vite-ignore */ fsName)) as { readFileSync(p: string, enc: 'utf8'): string }
    // import.meta.url is Vite's id here, not a file URL: vitest knows the real path.
    const dir = (expect.getState().testPath ?? '').replace(/[^/]*$/, '')
    const caps = fs.readFileSync(`${dir}CapabilitiesPanel.css`, 'utf8')
    const cc = fs.readFileSync(`${dir}SettingsDialog.css`, 'utf8')
    expect(caps).toMatch(/@media \(max-width: 899px\)[\s\S]*\.caps-dialog \{[^}]*height: 100%;[^}]*border-radius: 0;/)
    expect(cc).toMatch(/@media \(max-width: 640px\)[\s\S]*\.cc-pickers,\s*\.cc-tokens \{ grid-template-columns: 1fr; \}/)
    expect(cc).toMatch(/\.cc-footer \.btn \{ flex: 1; min-height: 40px; \}/)
  })
})

describe('SettingsDialog as a dialog', () => {
  it('names itself, holds Chat and Code colors, links to the main Settings, and has the old pickers inline', () => {
    render(<SettingsDialog onClose={() => {}} />)
    const dialog = screen.getByRole('dialog', { name: 'Settings' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(screen.getByRole('heading', { name: 'Chat' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Code colors' })).toBeInTheDocument()
    expect(screen.getByTestId('cc-theme-dark')).toBeInTheDocument()
    expect(screen.getByText('Customize')).toBeInTheDocument()
    const link = screen.getByRole('link', { name: /main Settings/ })
    expect(link.getAttribute('href')).toMatch(/\/settings$/)
  })

  it('closes on the ×, the backdrop and Escape, and puts focus back where it was', () => {
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()
    const onClose = vi.fn()
    const { unmount } = render(<SettingsDialog onClose={onClose} />)
    expect(screen.getByRole('dialog').contains(document.activeElement)).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose).toHaveBeenCalledTimes(1)
    fireEvent.pointerDown(screen.getByTestId("settings-overlay"))
    fireEvent.click(screen.getByTestId('settings-overlay'))
    expect(onClose).toHaveBeenCalledTimes(2)
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(3)
    unmount()
    expect(opener).toHaveFocus()
    opener.remove()
  })

  it('keeps Tab inside', () => {
    render(<SettingsDialog onClose={() => {}} />)
    const dialog = screen.getByRole('dialog')
    const nodes = Array.from(dialog.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), summary, [href]'))
    nodes[nodes.length - 1].focus()
    fireEvent.keyDown(dialog, { key: 'Tab' })
    expect(nodes[0]).toHaveFocus()
    fireEvent.keyDown(dialog, { key: 'Tab', shiftKey: true })
    expect(nodes[nodes.length - 1]).toHaveFocus()
  })
})
