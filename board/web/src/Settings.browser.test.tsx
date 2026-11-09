import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
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
    const models = await screen.findByRole('dialog', { name: 'Board models' })
    expect(models.querySelectorAll('select')).toHaveLength(8)
    expect(models.textContent).not.toContain('Repos')
    expect(models.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    fireEvent.click(screen.getByRole('button', { name: /Board settings/ }))
    await waitFor(() => expect(screen.getByLabelText('Board name')).toHaveValue('Example'))
    for (const name of ['General', 'Repos', 'Runs', 'Filed tickets', 'Models']) expect(screen.getByRole('heading', { name })).toBeVisible()

    const toggle = await screen.findByRole('checkbox', { name: 'Include component version on filed tickets' })
    await screen.findByRole('combobox', { name: 'Engine' })
    const retention = await screen.findByRole('spinbutton', { name: 'Auto-archive done after (days)' })
    expect(retention.getBoundingClientRect().width).toBeGreaterThan(0)
    expect(retention.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    fireEvent.change(retention, { target: { value: '30' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save archiving' }))
    const confirm = await screen.findByRole('button', { name: 'Confirm archiving' })
    expect(confirm.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    const dialog = screen.getByRole('dialog', { name: 'Board settings' })
    expect(dialog.getBoundingClientRect().width).toBeGreaterThan(width * 0.9)
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
