import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { FileGlyph, FolderGlyph } from './FileGlyph'

describe('legacy shared glyph API', () => {
  it('delegates to FileIcon and retains open/closed folder states', () => {
    const { container } = render(<><FileGlyph view="markdown" label="Markdown" /><FolderGlyph /><FolderGlyph expanded /></>)
    const icons = container.querySelectorAll('svg.file-icon')
    expect(icons).toHaveLength(3)
    expect(icons[0]).toHaveAttribute('data-file-icon', 'markdown')
    expect(icons[0].querySelector('title')).toHaveTextContent('Markdown')
    expect(icons[1]).toHaveAttribute('data-file-icon', 'folder')
    expect(icons[1].innerHTML).not.toBe(icons[2].innerHTML)
  })
})
