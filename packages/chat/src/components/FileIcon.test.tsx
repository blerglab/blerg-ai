import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { FileIcon } from './FileIcon'
import { fileIconPaths } from './fileIconPaths'

describe('shared FileIcon', () => {
  it('renders an inline, decorative SVG with a stable type, title and fixed size', () => {
    const { container } = render(<FileIcon name="src/main.go" label="Go source" />)
    const icon = container.querySelector('svg')!
    expect(icon).toHaveAttribute('data-file-icon', 'go')
    expect(icon).toHaveAttribute('aria-hidden', 'true')
    expect(icon).toHaveAttribute('focusable', 'false')
    expect(icon).toHaveAttribute('width', '16')
    expect(icon).toHaveAttribute('viewBox', '0 0 16 16')
    expect(icon.querySelector('title')).toHaveTextContent('Go source')
    expect(icon.querySelector('path')).toHaveAttribute('d', fileIconPaths.go)
    expect(container.querySelector('image, use, script')).toBeNull()
  })
  it('omits redundant titles on decorative icons so row text stays unchanged', () => {
    const { container } = render(<span><FileIcon name="main.go" />main.go</span>)
    expect(container.querySelector('svg title')).toBeNull()
    expect(container.textContent).toBe('main.go')
  })
  it('renders every curated glyph without external assets', () => {
    expect(Object.keys(fileIconPaths).length).toBeGreaterThanOrEqual(40)
    expect(Object.keys(fileIconPaths).length).toBeLessThanOrEqual(60)
    for (const path of Object.values(fileIconPaths)) {
      expect(path).toMatch(/^[Mm]/)
      expect(path).not.toMatch(/https?:|<|>/)
    }
  })
  it('retains folder identity and changes its silhouette when expanded', () => {
    const { container, rerender } = render(<FileIcon name="docs" directory />)
    const closed = container.querySelector('path')!.getAttribute('d')
    expect(container.querySelector('svg')).toHaveAttribute('data-file-icon', 'folder-docs')
    rerender(<FileIcon name="docs" directory expanded />)
    expect(container.querySelector('svg')).toHaveAttribute('data-file-icon', 'folder-docs')
    expect(container.querySelector('path')!.getAttribute('d')).not.toBe(closed)
    expect(container.querySelectorAll('path')).toHaveLength(2)
  })
})
