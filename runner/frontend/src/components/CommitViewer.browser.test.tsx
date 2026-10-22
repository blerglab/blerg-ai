// The commit viewer (card #131) uses the space on a phone and on a 4K screen: the diff viewer's frame,
// scaled with the viewport, never wider than the screen, and Maximize takes all of it.
import { describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { CommitViewer, type TransportFiles } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'

const SHA = 'a'.repeat(40)
const PATCH = `commit ${SHA}\nAuthor: Pat Example <pat@example.com>\nDate:   Wed, 21 Oct 2026 10:00:00 +0000\n\n    Read it on demand\n\ndiff --git a/lookup.go b/lookup.go\n--- a/lookup.go\n+++ b/lookup.go\n@@ -1 +1 @@\n-old\n+new\n`
const files = { list: vi.fn(), raw: vi.fn(), download: vi.fn(), remove: vi.fn(), upload: vi.fn(), commitDiff: async () => ({ text: PATCH }) } as unknown as TransportFiles

describe('CommitViewer at 390 px and 3840 px', () => {
  for (const [width, height] of [[390, 844], [3840, 2160]] as const) {
    it(`fits and scales at ${width} px`, async () => {
      await page.viewport(width, height)
      localStorage.clear()
      const { unmount } = render(<CommitViewer sessionId="s1" sha={SHA} files={files} repo="blerg" onClose={() => {}} />)
      const dialog = document.querySelector<HTMLElement>('[data-testid="commit-viewer-overlay"] [role="dialog"]')!
      await waitFor(() => expect(dialog.textContent).toContain('lookup.go'))
      const box = dialog.getBoundingClientRect()
      expect(box.left).toBeGreaterThanOrEqual(0)
      expect(box.right).toBeLessThanOrEqual(width)
      // It takes most of the screen, not a small fixed box.
      expect(box.width).toBeGreaterThan(width >= 3840 ? 1200 : width * 0.8)
      const max = dialog.querySelector<HTMLButtonElement>('button.artifact-maximize')!
      expect(max.getAttribute('aria-label')).toBe('Maximize')
      max.click()
      await waitFor(() => expect(dialog.getBoundingClientRect().width).toBeGreaterThanOrEqual(width - 1))
      max.click() // restore: the choice is remembered for the page
      await waitFor(() => expect(max.getAttribute('aria-label')).toBe('Maximize'))
      unmount()
    })
  }
})
