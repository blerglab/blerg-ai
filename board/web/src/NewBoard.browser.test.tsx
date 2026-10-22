// New board's repository picker, in Chromium at a phone's width and at 4K.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { page, userEvent } from 'vitest/browser'
import { Boards } from './App'
import { MeProvider } from './me'
import { api } from './api'
import './styles.css'

const choices = Array.from({ length: 14 }, (_, i) => ({
  repo: `example-org/a-repository-with-a-rather-long-name-${i}`, folder: `a-repository-with-a-rather-long-name-${i}`, desktops: ['A workstation with a long machine name'],
}))
vi.mock('./api', async original => ({
  ...await original<typeof import('./api')>(),
  api: vi.fn(async (path: string) => {
    if (path === '/api/me') return { is_admin: true, is_human: true, kind: 'human' }
    if (path === '/api/repo-choices') return { available: true, repos: choices }
    if (path === '/api/boards') return []
    return []
  }),
}))

describe('New board repositories in Chromium', () => {
  afterEach(cleanup)
  it.each([390, 3840])('fits and picks repositories at %i px', async width => {
    const height = width === 3840 ? 2160 : 844
    await page.viewport(width, height)
    const { container } = render(<MemoryRouter><MeProvider><Boards /></MeProvider></MemoryRouter>)
    await userEvent.click(await screen.findByText('New board'))
    await userEvent.fill(screen.getByPlaceholderText('board name'), 'launch')
    await userEvent.fill(await screen.findByLabelText('Find a repository'), 'name-1')
    const list = screen.getByRole('list', { name: 'Repositories on your desktops' })
    // name-1 and name-10 … name-13
    expect(list.querySelectorAll('input[type=checkbox]').length).toBe(5)
    await userEvent.click(screen.getByRole('checkbox', { name: /long-name-12/ }))
    await userEvent.click(screen.getByRole('checkbox', { name: /long-name-1 / }))
    expect(screen.getByLabelText('Chosen repositories').textContent).toMatch(/long-name-12 primary/)
    const form = container.querySelector('form.new-board') as HTMLElement
    const box = form.getBoundingClientRect()
    expect(box.right).toBeLessThanOrEqual(width)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    // The form uses the space on a large screen instead of a small fixed cap.
    if (width === 3840) expect(box.width).toBeGreaterThan(960)
    for (const el of form.querySelectorAll<HTMLElement>('button, select, input[type=text], input[type=search]')) {
      const r = el.getBoundingClientRect()
      expect(r.right).toBeLessThanOrEqual(width)
      if (width === 390) expect(r.height).toBeGreaterThanOrEqual(44)
    }
    // The choices list scrolls within a height that scales with the screen.
    expect(parseFloat(getComputedStyle(list).maxHeight)).toBeCloseTo(Math.max(160, height * 0.3), 0)
    await userEvent.click(screen.getByText('Create board'))
    const call = vi.mocked(api).mock.calls.find(c => c[0] === '/api/boards' && (c[1] as { json?: unknown })?.json)
    expect((call?.[1] as { json: Record<string, unknown> }).json.repos).toEqual([choices[12].repo, choices[1].repo])
    vi.mocked(api).mockClear()
  })
})
