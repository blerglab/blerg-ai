import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import Markdown from './Markdown'
import { createCardRegistry, defineCard } from '../cards/registry'
import { testing } from '../model/highlight'

const lines = (n: number) => Array.from({ length: n }, (_, i) => `line ${i + 1}`).join('\n')
const fence = (body: string, lang = '') => '```' + lang + '\n' + body + '\n```'

beforeEach(() => testing.reset(() => import('../model/highlighter')))
afterEach(() => vi.restoreAllMocks())

describe('Markdown code blocks', () => {
  it('highlights a closed fence once the highlighter has loaded, as spans', async () => {
    const { container } = render(<Markdown text={fence('func main() { return "x" }', 'go')} />)
    expect(container.querySelector('.hljs-keyword')).toBeNull()
    await waitFor(() => expect(container.querySelector('.hljs-keyword')?.textContent).toBe('func'))
    expect(container.querySelector('pre')?.textContent).toBe('func main() { return "x" }')
  })

  it('shows an unknown language plain', async () => {
    const { container } = render(<Markdown text={fence('func main() {}', 'klingon')} />)
    await new Promise(r => setTimeout(r, 20))
    expect(container.querySelector('.hljs-keyword')).toBeNull()
    expect(container.querySelector('pre')?.textContent).toContain('func main()')
  })

  it('never highlights or caps a fence that is still streaming', async () => {
    const { container } = render(<Markdown text={'```go\n' + lines(40)} />)
    await new Promise(r => setTimeout(r, 20))
    expect(container.querySelector('.md-codeblock-actions')).toBeNull()
    expect(container.querySelector('pre')?.textContent).toContain('line 40')
    expect(container.querySelector('[class^="hljs-"]')).toBeNull()
  })

  it('leaves a block of 25 lines whole', () => {
    const { container } = render(<Markdown text={fence(lines(25), 'text')} />)
    expect(container.querySelector('.md-codeblock-actions')).toBeNull()
    expect(container.querySelector('pre')?.textContent).toContain('line 25')
  })

  it('shows the first 15 lines of a block over 25, expands, collapses and opens', () => {
    const { container } = render(<Markdown text={fence(lines(40), 'text')} />)
    const pre = () => container.querySelector('.md-codeblock pre')!.textContent!
    expect(pre()).toContain('line 15')
    expect(pre()).not.toContain('line 16')
    fireEvent.click(screen.getByRole('button', { name: 'Show all 40 lines' }))
    expect(pre()).toContain('line 40')
    fireEvent.click(screen.getByRole('button', { name: 'Show less' }))
    expect(pre()).not.toContain('line 16')
    fireEvent.click(screen.getByRole('button', { name: 'Open' }))
    const dialog = screen.getByRole('dialog')
    expect(dialog.textContent).toContain('40 lines')
    // Plain text reads as prose and wraps by default (#265): one numbered row per line.
    expect([...dialog.querySelectorAll('.code-line')].map(row => row.getAttribute('data-n'))).toEqual(Array.from({ length: 40 }, (_, i) => String(i + 1)))
    expect(dialog.querySelector('.code-viewer-code')?.textContent).toContain('line 40')
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('copies the whole block even when it is capped', async () => {
    const writeText = vi.fn(() => Promise.resolve())
    Object.assign(navigator, { clipboard: { writeText } })
    const body = lines(40)
    render(<Markdown text={fence(body, 'text')} />)
    fireEvent.click(screen.getByLabelText('Copy code'))
    expect(writeText).toHaveBeenCalledWith(body)
  })

  it('colours a diff fence by line', () => {
    const { container } = render(<Markdown text={fence('@@ -1 +1 @@\n-old\n+new\n same', 'diff')} />)
    expect(container.querySelector('.md-diff-del')?.textContent).toContain('-old')
    expect(container.querySelector('.md-diff-add')?.textContent).toContain('+new')
    expect(container.querySelector('.md-diff-hunk')).not.toBeNull()
  })

  it('keeps card fences as cards', () => {
    const Card = ({ data }: { data: Record<string, unknown> }) => <b data-testid="c">{String(data.x)}</b>
    const cards = createCardRegistry([defineCard('thing', Card)])
    render(<Markdown text={fence('{"x": 1}', 'card:thing')} cards={cards} />)
    expect(screen.getByTestId('c').textContent).toBe('1')
  })
})
