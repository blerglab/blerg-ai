import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import './styles.css'
import { Tokens } from './Tokens'
import { BoardSettings } from './BoardSettings'
import { MeProvider } from './me'
import type { Board } from './api'

vi.mock('./api', async original => ({
  ...await original<typeof import('./api')>(),
  api: vi.fn(async (path: string) => path === '/api/me'
    ? { is_admin: true, is_human: true }
    : path === '/api/tokens' || path === '/api/boards' ? [] : path.includes('launch-options') ? { state: 'known', engines: [{ id: 'codex', name: 'Codex', models: { models: [] } }, { id: 'claude', name: 'Claude', models: { models: [] } }] } : {}),
}))
afterEach(cleanup)

describe('Board settings primitives', () => {
  it.each([390, 3840])('fits and styles controls at %i px', async width => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    const board = { id: 'b1', name: 'Example', repos: [], concurrency: 1, ci_policy: 'required', automation_token_set: true, automation_engine: 'codex' } as unknown as Board
    render(<MeProvider><BoardSettings board={board} /></MeProvider>)
    fireEvent.click(screen.getByRole('button', { name: /model/ }))
    const toggle = await screen.findByRole('checkbox', { name: 'Include component version on filed tickets' })
    await screen.findByRole('combobox', { name: 'Engine' })
    const dialog = screen.getByRole('dialog', { name: 'Board settings' })
    expect(getComputedStyle(toggle).appearance).toBe('none')
    expect(toggle.getBoundingClientRect().width).toBeLessThan(60)
    expect(dialog.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth + 1)
    for (const b of dialog.querySelectorAll('button')) expect(b.classList.contains('settings-action')).toBe(true)
    const shots = (import.meta.env as Record<string, string | undefined>).VITE_SETTINGS_SHOTS
    if (shots) await page.screenshot({ path: `${shots}/board-settings-${width}.png`, element: dialog })
  })
})

describe('Board token administration', () => {
  it.each([390, 3840])('styles token controls at %i px', async width => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    render(<MeProvider><Tokens /></MeProvider>)
    const mint = await screen.findByRole('button', { name: 'Mint token' })
    expect(mint).toHaveClass('settings-action')
    for (const select of document.querySelectorAll('select')) {
      expect(select).toHaveClass('settings-field')
      expect(select.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    }
    const shots = (import.meta.env as Record<string, string | undefined>).VITE_SETTINGS_SHOTS
    if (shots) await page.screenshot({ path: `${shots}/board-tokens-${width}.png` })
  })
})
