import { expect, it } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { ChatView } from '@blerglab/chat'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../../../../packages/chat/src/test/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

// Card #275: a Blerg notice shows inline Markdown, never raw backticks or asterisks, in one compact row.
for (const width of [390, 3840]) {
  it(`draws a Blerg notice as inline Markdown inside ${width}px`, async () => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    resetChatState()
    const text = `Child "#258 search ignores: fix round" finished its turn: "Head: \`3e6f638c\`. Commits: \`13fa459c\`, **Redesign — 3 high** ${'x'.repeat(120)} <img src=x onerror=alert(1)> ![p](https://example.com/a.png)"`
    seed(ev({ seq: 1, kind: 'user_message', payload: { source: 'child_turn', text } }))
    render(<div style={{ height: '100vh' }}><ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'idle' })} /></div>)
    await waitFor(() => expect(screen.getByTestId('system-note')).toBeTruthy())
    const row = screen.getByTestId('system-note')
    expect(row.textContent).not.toContain('`')
    expect(row.textContent).not.toContain('**')
    expect(Array.from(row.querySelectorAll('code')).map(c => c.textContent)).toEqual(['3e6f638c', '13fa459c'])
    expect(row.querySelector('strong')?.textContent).toBe('Redesign — 3 high')
    expect(row.querySelector('img')).toBeNull()
    const bounds = row.getBoundingClientRect()
    expect(bounds.left).toBeGreaterThanOrEqual(0)
    expect(bounds.right).toBeLessThanOrEqual(width)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    expect(bounds.height).toBeLessThan(width === 390 ? 380 : 70)
  })
}
