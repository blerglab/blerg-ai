import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import ThemeToggle from './ThemeToggle'
import { THEMES } from '../../../packages/settings/theme'
import './styles.css'
import '@blerglab/chat/blerg.css'
const request = vi.hoisted(() => vi.fn())
vi.mock('./api', () => ({api: request}))
vi.mock('./authClient', () => ({getAccessToken: () => 'fixture'}))
afterEach(() => { cleanup(); document.documentElement.removeAttribute('data-theme'); localStorage.clear() })
it.each([390, 3840])('keeps the account theme selector and chat palette visible at %i px', async width => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 request.mockImplementation(async (_url: string, init?: {json?: unknown}) => init?.json || {theme:'dark'})
 render(<header className="topbar"><span className="wordmark">blerg board</span><span className="build-version">v0.6.0</span><nav><a href="#boards">Boards</a><a href="#gate">Gate</a><a href="#tokens">Tokens</a><a href="#onboard">Onboard</a><ThemeToggle /></nav></header>)
 const select=screen.getByRole('combobox', {name:'Theme'})
 await waitFor(() => expect(document.documentElement.dataset.theme).toBe('dark'))
 for (const {id} of THEMES.filter(theme => theme.id !== 'system')) {
  fireEvent.change(select, {target:{value:id}})
  await waitFor(() => expect(document.documentElement.dataset.theme).toBe(id))
  await waitFor(() => expect(select).not.toBeDisabled())
  const css=getComputedStyle(document.documentElement)
  expect(css.getPropertyValue('--chat-fg').trim()).toBe(css.getPropertyValue('--chalk').trim())
  expect(css.getPropertyValue('--chat-diff-add').trim()).toBe(css.getPropertyValue('--diff-add').trim())
  expect(select.getBoundingClientRect().right).toBeLessThanOrEqual(width)
  expect(select.getBoundingClientRect().left).toBeGreaterThanOrEqual(0)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
 }
})
