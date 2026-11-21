import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import App from './App'
import { denseCards, parityBoard, parityColumns } from '../../../packages/board/parityFixture'
import './styles.css'
vi.mock('./api', async original => ({ ...await original<typeof import('./api')>(), boardSocket: () => () => {}, api: vi.fn(async (path: string) => {
 if (path === '/api/me') return { kind: 'human', is_admin: true, is_human: true, account_id: 'focus-person' }
 if (path === '/api/me/theme') return { theme: 'dark' }
 if (path === '/api/boards') return [parityBoard]
 if (path === '/api/boards/parity') return parityBoard
 if (path.endsWith('/columns')) return parityColumns
 if (path.includes('/cards/view')) return { cards: denseCards, older: {} }
 if (path.endsWith('/run')) return { state: 'stopped', ready: 0, blocked: 0, in_flight: 0 }
 return []
}) }))
afterEach(() => { cleanup(); window.history.replaceState({}, '', '/') })
it.each([390, 1440, 3840])('focuses 80 tickets through the Board app at %s', async width => {
 localStorage.clear(); sessionStorage.clear()
 document.documentElement.setAttribute('data-theme', 'dark')
 await page.viewport(width, width === 390 ? 844 : 2160)
 window.history.replaceState({}, '', '/boards/parity')
 render(<App />)
 await screen.findAllByText(denseCards[0].title)
 if (width === 390) fireEvent.click(screen.getByRole('tab', { name: /In progress/ }))
 const shots = import.meta.env.VITE_BOARD_SHOTS_DIR
 if (shots) await page.screenshot({ path: `${shots}/board-before-focus-${width}.png` })
 fireEvent.click(screen.getByRole('button', { name: 'Focus In progress' }))
 await waitFor(() => expect(document.querySelectorAll('.shared-board-column')).toHaveLength(1))
 expect(document.querySelectorAll('.shared-board-card')).toHaveLength(80)
 for (const card of document.querySelectorAll('.shared-board-card')) { const { width: cardWidth } = card.getBoundingClientRect(); expect(cardWidth).toBeGreaterThanOrEqual(Math.min(268, width - 80)); expect(cardWidth).toBeLessThan(Math.max(536, width)) }
 if (shots) await page.screenshot({ path: `${shots}/board-focus-${width}.png` })
 fireEvent.click(screen.getByRole('button', { name: 'Back to all columns' }))
 expect(document.querySelectorAll('.shared-board-column')).toHaveLength(4)
 fireEvent.click(screen.getByRole('button', { name: 'Focus In progress' }))
 fireEvent.keyDown(document, { key: 'Escape' })
 expect(document.querySelectorAll('.shared-board-column')).toHaveLength(4)
})
