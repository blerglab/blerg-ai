import { afterEach, describe, expect, it } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { ArtifactViewer, DiffView, type TransportFiles } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

const patch = Array.from({length: 5}, (_, file) => `diff --git a/src/file-${file}.txt b/src/file-${file}.txt\n--- a/src/file-${file}.txt\n+++ b/src/file-${file}.txt\n@@ -1 +1,100 @@\n-old\n${Array.from({length: 100}, (_, line) => `+file-${file}-line-${line}`).join('\n')}\n`).join('')
const files: TransportFiles = {list: async () => [], raw: async () => new Blob([patch]), download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') }}

afterEach(async () => { await page.viewport(1280, 800) })

async function mount(text = patch) {
  render(<ArtifactViewer sessionId="scroll-fixture" files={{...files, raw: async () => new Blob([text])}} artifact={{id: 'patch', name: 'changes.patch', size: patch.length, view: 'diff', content_type: 'text/x-diff'}} onClose={() => {}} />)
  await waitFor(() => expect(document.querySelector('.diff-main')).not.toBeNull())
  const code = document.querySelector<HTMLElement>('.diff-main')!
  const buttons = [...document.querySelectorAll<HTMLButtonElement>('.diff-files-item')]
  const sections = [...code.querySelectorAll<HTMLElement>(':scope > .diff-file')]
  return {code, buttons, sections}
}

describe('diff file scroll position', () => {
  it.each([1280, 390])('tracks clicked and manually scrolled files, including both ends at %ipx', async width => {
    await page.viewport(width, 800)
    const {code, buttons, sections} = await mount()
    await waitFor(() => expect(buttons[0].getAttribute('aria-current')).toBe('location'))
    await userEvent.click(buttons[1])
    await waitFor(() => expect(buttons[1].getAttribute('aria-current')).toBe('location'))
    code.scrollTop += sections[2].getBoundingClientRect().top - code.getBoundingClientRect().top + 10
    await waitFor(() => expect(buttons[2].getAttribute('aria-current')).toBe('location'))
    expect(buttons[1].hasAttribute('aria-current')).toBe(false)
    code.scrollTop = code.scrollHeight
    await waitFor(() => expect(buttons[4].getAttribute('aria-current')).toBe('location'))
    expect(getComputedStyle(buttons[4]).boxShadow).not.toBe('none')
    await waitFor(() => expect(buttons[4].getBoundingClientRect().bottom).toBeLessThanOrEqual(buttons[4].closest('nav')!.getBoundingClientRect().bottom + 1))
    code.scrollTop = 0
    await waitFor(() => expect(buttons[0].getAttribute('aria-current')).toBe('location'))
    expect(document.querySelectorAll('.diff-files [aria-current=location]').length).toBe(1)
  })

  it('recomputes the visible file when a section collapses and expands', async () => {
    const {code, buttons, sections} = await mount()
    await userEvent.click(buttons[1])
    await waitFor(() => expect(buttons[1].getAttribute('aria-current')).toBe('location'))
    const toggle = sections[1].querySelector<HTMLButtonElement>('.diff-file-toggle')!
    await userEvent.click(toggle)
    code.scrollTop += sections[2].getBoundingClientRect().top - code.getBoundingClientRect().top + 10
    await waitFor(() => expect(buttons[2].getAttribute('aria-current')).toBe('location'))
    await userEvent.click(buttons[1])
    await userEvent.click(toggle)
    await waitFor(() => expect(buttons[1].getAttribute('aria-current')).toBe('location'))
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
  })
  it('tracks each diff code pane independently', async () => {
    const {container} = render(<><div style={{height: 350, display: 'flex'}}><DiffView text={patch} /></div><div style={{height: 350, display: 'flex'}}><DiffView text={patch} /></div></>)
    const panes = [...container.querySelectorAll<HTMLElement>('.diff-main')]
    const navs = [...container.querySelectorAll<HTMLElement>('.diff-files')]
    panes[1].scrollTop = panes[1].scrollHeight
    await waitFor(() => expect(navs[1].querySelector('[aria-current=location]')?.getAttribute('title')).toBe('src/file-4.txt'))
    expect(navs[0].querySelector('[aria-current=location]')?.getAttribute('title')).toBe('src/file-0.txt')
  })

  it('marks the next visible file when its commit heading reaches the top', async () => {
    const text = patch.replace('diff --git a/src/file-1.txt', `commit ${'a'.repeat(40)}\nAuthor: Example\nDate: 2026-10-19\n\n    Next commit\n\ndiff --git a/src/file-1.txt`)
    const {code, buttons} = await mount(text)
    const head = code.querySelector<HTMLElement>('.diff-commit')!
    code.scrollTop += head.getBoundingClientRect().top - code.getBoundingClientRect().top
    await waitFor(() => expect(buttons[1].getAttribute('aria-current')).toBe('location'))
  })

})
