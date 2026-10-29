// Card #229 in a real browser: buttons the button guard found bare now draw in the app style (no
// browser default look) on a phone and on a 4K screen. The static guard is in SettingsPrimitives.test.ts.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import '../index.css'
import './SessionRail.css'
import ModelCatalogNotice from './ModelCatalogNotice'
import { StartPluginSession } from './SessionPlugins'

describe('buttons the guard fixed', () => {
  it.each([390, 3840])('draw in the app style at %i px', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    render(<div style={{ display: 'grid', gap: 12, padding: 16, background: 'var(--basalt)', color: 'var(--chalk)' }} data-testid="shots">
      <ModelCatalogNotice state={{ models: [], error: 'Could not list models: the engine is not signed in.', retry: () => {}, refreshing: false } as never} />
      <p role="status">Plugin added. <StartPluginSession /></p>
      <span className="reel-chips"><button type="button" className="reel-chip stroke-card">#42</button> <button type="button" className="reel-chip reel-card-chip">Board #7 Fix the build</button></span>
    </div>)
    const app = document.createElement('button')
    app.className = 'btn small'
    document.body.append(app)
    for (const name of ['Retry models', 'Start a new session with it']) {
      const button = screen.getByRole('button', { name })
      const cs = getComputedStyle(button), ref = getComputedStyle(app)
      expect(cs.backgroundColor).toBe(ref.backgroundColor)
      expect(cs.borderTopWidth).toBe('1px')
      expect(cs.borderTopLeftRadius).toBe('6px')
      expect(cs.fontFamily).toBe(getComputedStyle(document.body).fontFamily)
      expect(button.getBoundingClientRect().right).toBeLessThanOrEqual(width)
      expect(button.getBoundingClientRect().height).toBeGreaterThanOrEqual(width < 640 ? 36 : 26)
    }
    // History's card chips: transparent like every other chip, not the browser's button fill.
    for (const name of ['#42', 'Board #7 Fix the build']) expect(getComputedStyle(screen.getByRole('button', { name })).backgroundColor).toBe('rgba(0, 0, 0, 0)')
    app.remove()
    const shots = (import.meta.env as Record<string, string | undefined>).VITE_BUTTON_SHOTS
    if (shots) await page.screenshot({ path: `${shots}/runner-buttons-${width}.png`, element: screen.getByTestId('shots') })
  })
})
