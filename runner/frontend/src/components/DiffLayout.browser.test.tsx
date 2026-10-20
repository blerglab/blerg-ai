import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { ArtifactViewer, ChatContext, type TransportFiles, type ChatContextValue } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

const patch = Array.from({length: 90}, (_, file) => `diff --git a/src/file-${file}.txt b/src/file-${file}.txt\n--- a/src/file-${file}.txt\n+++ b/src/file-${file}.txt\n@@ -1,1 +1,${file ? 1 : 180} @@\n-old\n${Array.from({length: file ? 1 : 180}, (_, line) => `+line-${line}-${'x'.repeat(300)}`).join('\n')}\n`).join('')
const files: TransportFiles = {list: async () => [], raw: async () => new Blob([patch]), download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') }}
const context: ChatContextValue = {sessionId: 'layout-fixture', files: null, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined, reviewFor: async () => null, submitFeedback: async () => true}

afterEach(async () => { await page.viewport(1280, 800) })

describe('diff review scroll boundaries', () => {
  it.each([1280, 390].flatMap(width => [1, 90].map(count => ({width, count}))))('keeps navigation visible and independently scrolls $count files at $width px', async ({width, count}) => {
    await page.viewport(width, 800)
    const text = count === 1 ? patch.slice(0, patch.indexOf('diff --git', 10)) : patch
    render(<ChatContext.Provider value={context}><ArtifactViewer sessionId="layout-fixture" files={{...files, raw: async () => new Blob([text])}} artifact={{id: 'patch', name: 'changes.patch', size: patch.length, view: 'diff', content_type: 'text/x-diff'}} onClose={() => {}} /></ChatContext.Provider>)
    await waitFor(() => expect(document.querySelector('.diff-files')).not.toBeNull())
    const nav = document.querySelector<HTMLElement>('.diff-files')!
    const code = document.querySelector<HTMLElement>('.diff-main')!
    if (count > 1) expect(nav.scrollHeight).toBeGreaterThan(nav.clientHeight)
    expect(code.scrollHeight).toBeGreaterThan(code.clientHeight)
    const top = nav.getBoundingClientRect().top
    code.scrollTop = 180
    await new Promise(resolve => requestAnimationFrame(resolve))
    expect(code.scrollTop).toBeGreaterThan(0)
    expect(nav.scrollTop).toBe(0)
    expect(nav.getBoundingClientRect().top).toBe(top)
    if (count > 1) {
      nav.scrollTop = 100
      expect(nav.scrollTop).toBeGreaterThan(0)
    }
    expect(code.scrollTop).toBe(180)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
  })
  it.each([1280, 390])('retains code-only horizontal panning at %ipx', async width => {
    await page.viewport(width, 800)
    render(<ArtifactViewer sessionId="layout-fixture" files={files} artifact={{id: 'patch', name: 'long-' + 'x'.repeat(500) + '.patch', size: patch.length, view: 'diff', content_type: 'text/x-diff'}} onClose={() => {}} />)
    await waitFor(() => expect(document.querySelector('.diff-table-wrap')).not.toBeNull())
    const table = document.querySelector<HTMLElement>('.diff-table-wrap')!
    expect(table.scrollWidth).toBeGreaterThan(table.clientWidth)
    table.scrollLeft = 100
    expect(table.scrollLeft).toBeGreaterThan(0)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
  })
})
