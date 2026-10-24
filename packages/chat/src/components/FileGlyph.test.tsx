import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { FileGlyph, FolderGlyph } from './FileGlyph'
describe('shared Files glyphs', () => {
  it('retains the Files list page and type marks and gives folders the same canvas and stroke', () => {
    const { container } = render(<><FileGlyph view="markdown" label="Markdown" /><FolderGlyph /><FolderGlyph expanded /></>)
    const icons = container.querySelectorAll('svg')
    expect(icons).toHaveLength(3)
    expect(icons[0].querySelector('path')?.getAttribute('d')).toBe('M3.5 1.5h6L13 5v9.5H3.5zM9.5 1.5V5H13')
    expect(icons[0].querySelectorAll('path')[1].getAttribute('d')).toBe('M6 11V8l1.5 2L9 8v3')
    for (const icon of icons) { expect(icon.getAttribute('viewBox')).toBe('0 0 16 16'); expect(icon.querySelector('path')?.getAttribute('stroke-width')).toBe('1') }
    expect(icons[1].innerHTML).not.toBe(icons[2].innerHTML)
  })
})
