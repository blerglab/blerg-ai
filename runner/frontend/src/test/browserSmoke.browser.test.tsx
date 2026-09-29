import { describe, it, expect } from 'vitest'

// Smoke test: confirms the real-browser (Playwright/Chromium) project launches
// and that real layout measurements are available (the whole reason this project
// exists — jsdom returns 0 for these).
describe('browser project smoke', () => {
  it('runs in a real browser with a working layout engine', () => {
    const el = document.createElement('div')
    el.style.cssText = 'height:50px;overflow:auto'
    const tall = document.createElement('div')
    tall.style.height = '500px'
    el.appendChild(tall)
    document.body.appendChild(el)
    // jsdom would report 0 here; a real browser reports real pixels.
    expect(el.clientHeight).toBe(50)
    expect(el.scrollHeight).toBeGreaterThan(el.clientHeight)
    document.body.removeChild(el)
  })
})
