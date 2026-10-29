import { expect } from 'vitest'
import { detectFileIcon } from '@blerglab/chat'
import { fileIconPaths } from '../../../../packages/chat/src/components/fileIconPaths'

/** Attachment previews may contain our static icons, never user-supplied SVG.
 * Check exact curated geometry and allow only inert attributes, so adding icons
 * does not weaken the adversarial preview fixtures' SVG rejection coverage.
 */
export function expectOnlyFileIcons(container: HTMLElement) {
  const icons = container.querySelectorAll('svg')
  expect(icons.length).toBeGreaterThan(0)
  const allowed = new Set(['class', 'data-file-icon', 'viewBox', 'width', 'height', 'aria-hidden', 'focusable', 'style'])
  for (const icon of icons) {
    const name = icon.closest('.attach-chip')?.querySelector('.attach-chip-name')?.textContent
    expect(name).toBeTruthy()
    const kind = detectFileIcon(name!)
    expect(icon.getAttribute('data-file-icon')).toBe(kind)
    expect(icon.getAttribute('aria-hidden')).toBe('true')
    expect(icon.getAttribute('focusable')).toBe('false')
    expect([...icon.attributes].every(attr => allowed.has(attr.name))).toBe(true)
    if (icon.hasAttribute('style')) expect(icon.getAttribute('style')).toMatch(/^--chat-file-icon-color: #[a-f\d]{6};$/i)
    expect(icon.children).toHaveLength(1)
    const path = icon.firstElementChild!
    expect(path.tagName).toBe('path')
    expect(path.getAttribute('d')).toBe(fileIconPaths[kind])
    expect(path.getAttribute('fill')).toBe('currentColor')
    expect([...path.attributes].map(attr => attr.name).sort()).toEqual(['d', 'fill'])
  }
}
