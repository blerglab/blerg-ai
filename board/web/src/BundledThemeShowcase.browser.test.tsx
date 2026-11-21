// BLG-474: real-app evidence that the bundled open-source schemes render legibly on the board,
// not just that the tokens pass their contrast math. Mounts the real shared board (same harness as
// BoardParity.browser.test.tsx) in three of the six bundled schemes, at phone and very-wide
// widths. Screenshots only when VITE_THEME_SHOTS_DIR is set (owner review evidence).
import { afterEach, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { vi } from 'vitest'
import { BoardView } from './BoardView'
import { MeProvider } from './me'
import { parityBoard, parityCards, parityColumns } from '../../../packages/board/parityFixture'
import './styles.css'

let destination = 'lane-0'
vi.mock('./api', async original => ({
  ...await original<typeof import('./api')>(),
  boardSocket: () => () => {},
  api: vi.fn(async (path: string) => {
    if (path === '/api/me') return { kind: 'human', is_admin: true, is_human: true, account_id: 'theme-preview' }
    if (path === '/api/boards/parity') return parityBoard
    if (path.endsWith('/columns')) return parityColumns
    if (path.includes('/cards/view')) return { cards: parityCards.map(c => c.number === 1 ? { ...c, column_id: destination } : c), older: {} }
    if (path === '/api/cards/card-1') return { ...parityCards[0], column_id: destination }
    if (path.endsWith('/run')) return { state: 'stopped', ready: 0, blocked: 0, in_flight: 0 }
    return []
  }),
}))
afterEach(cleanup)

const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_THEME_SHOTS_DIR
const THEMES_TO_SHOW = ['catppuccin-mocha', 'nord', 'tokyo-night'] as const

for (const theme of THEMES_TO_SHOW) {
  for (const width of [390, 3840]) {
    it(`renders the board readably in ${theme} at ${width}px`, async () => {
      localStorage.clear(); sessionStorage.clear()
      destination = 'lane-0'
      document.documentElement.setAttribute('data-theme', theme)
      await page.viewport(width, width === 390 ? 844 : 1200)
      render(<MemoryRouter><MeProvider><BoardView boardId="parity" /></MeProvider></MemoryRouter>)
      await screen.findByText(parityCards[0].title)
      if (shotDir) await page.screenshot({ path: `${shotDir}/board-${theme}-${width}.png` })
      document.documentElement.removeAttribute('data-theme')
    })
  }
}
