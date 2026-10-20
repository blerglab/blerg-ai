import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { renderEvent } from './EventCards'
import { ev } from '../../test/chat'

function thinking(text: string) {
  return renderEvent(ev({ kind: 'assistant_thinking', payload: { text } }), new Map())
}

describe('thinking Markdown', () => {
  it('formats code, emphasis and lists while retaining its thinking identity', () => {
    render(thinking('Inspect `intervals.py` and **preserve** the endpoints.\n\n- Read the tests\n- Fix the merge\n\n```python\nend = max(end, next_end)\n```'))
    const card = screen.getByTestId('thinking')
    expect(card.dataset.kind).toBe('thinking')
    expect(card.querySelector('code')?.textContent).toBe('intervals.py')
    expect(card.querySelector('strong')?.textContent).toBe('preserve')
    expect(within(card).getAllByRole('listitem')).toHaveLength(2)
    expect(card.querySelector('pre code')?.textContent).toContain('end = max(end, next_end)')
    expect(card.querySelector('.card-foot')?.textContent).toContain('thinking')
    expect(card.querySelector('.thinking-text')?.classList.contains('md')).toBe(true)
  })

  it('keeps untrusted HTML, unsafe links and remote images inert', () => {
    const { container } = render(thinking('<script>alert(1)</script>\n\n<img src="https://example.invalid/track" onerror="alert(1)">\n\n[unsafe](javascript:alert%281%29)\n\n![remote](https://example.invalid/pixel.png)'))
    expect(container.querySelector('script, img, iframe')).toBeNull()
    expect(container.querySelector('a[href^="javascript:"]')).toBeNull()
    expect(screen.getByTestId('thinking').textContent).toContain('unsafe')
  })

  it('renders an unfinished streamed fence and updates it without losing the thinking label', () => {
    const { container, rerender } = render(thinking('```python\nvalue = 1'))
    expect(container.querySelector('pre code')?.textContent).toContain('value = 1')
    rerender(thinking('```python\nvalue = 12\n```\n\n**Checked.**'))
    expect(container.querySelector('pre code')?.textContent).toContain('value = 12')
    expect(container.querySelector('strong')?.textContent).toBe('Checked.')
    expect(screen.getByTestId('thinking').textContent).toContain('thinking')
  })
})
