import { afterEach, describe, expect, it } from 'vitest'
import { act, render } from '@testing-library/react'
import SourceCode from './SourceCode'
import { resetLineWrap, setLineWrap } from '../model/lineWrap'

afterEach(() => { resetLineWrap(); localStorage.clear() })
const TEXT = 'first line\nsecond line that is long\nthird\n'

describe('SourceCode line wrap (#265)', () => {
  it('wraps prose by default: one numbered row per line, the text unchanged', () => {
    const { container } = render(<SourceCode text={TEXT} name="notes.txt" />)
    expect(container.querySelector('[data-testid="workspace-code"]')).toHaveAttribute('data-wrap', 'on')
    expect(container.querySelector('.code-viewer-gutter')).toBeNull()
    expect([...container.querySelectorAll('.code-line')].map(l => l.getAttribute('data-n'))).toEqual(['1', '2', '3'])
    expect(container.querySelector('code')!.textContent).toBe(TEXT)
  })

  it('leaves code unwrapped by default and follows the setting', () => {
    const { container } = render(<SourceCode text={TEXT} name="main.ts" />)
    expect(container.querySelector('[data-testid="workspace-code"]')).toHaveAttribute('data-wrap', 'off')
    expect(container.querySelector('.code-viewer-gutter')).toBeTruthy()
    act(() => setLineWrap('code', true))
    expect(container.querySelector('[data-testid="workspace-code"]')).toHaveAttribute('data-wrap', 'on')
    expect(container.querySelector('code')!.textContent).toBe(TEXT)
  })

  it('marks the named line on its row when wrapped', () => {
    const { container } = render(<SourceCode text={TEXT} name="notes.txt" line={2} />)
    expect(container.querySelector('[data-testid="workspace-line"]')).toHaveAttribute('data-n', '2')
  })
})
