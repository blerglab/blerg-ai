import { render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { ArtifactViewer, WorkspaceFileViewer, type TransportFiles } from '@blerglab/chat'
import '../index.css'
import '@blerglab/chat/blerg.css'

const cases = [
  ['notes.md', 'markdown', '# Hello\n\n**world**'],
  ['page.html', 'html', '<h1>Hello</h1>'],
  ['table.csv', 'csv', 'name,count\nhello,2'],
  ['chart.mmd', 'text', 'graph TD\n A-->B'],
  ['chart.vl.json', 'json', '{"mark":"bar","data":{"values":[]}}'],
] as const
afterEach(() => document.documentElement.style.removeProperty('--chat-code-keyword'))
for (const width of [390, 3840]) for (const kind of ['workspace', 'published']) {
  for (const [name, view, text] of cases) it(`toggles ${kind} ${name} source and rendering at ${width}`, async () => {
    await page.viewport(width, 900)
    const files: TransportFiles = {
      list: async () => [], raw: async () => new Blob([text]), download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') },
      workspaceFile: async () => ({ path: name, mtime: '', view, content_type: 'text/plain', size: text.length, blob: new Blob([text]) }),
    }
    const props = { sessionId: 'source', files, onClose: () => {} }
    render(kind === 'workspace' ? <WorkspaceFileViewer {...props} path={name} /> : <ArtifactViewer {...props} artifact={{ id: name, name, view, content_type: 'text/plain', size: text.length }} />)
    const dialog = screen.getByRole('dialog', { name })
    await userEvent.click(await within(dialog).findByRole('button', { name: 'Raw' }))
    await waitFor(() => expect(dialog.querySelector('[class*="hljs-"]')).not.toBeNull(), { timeout: 10000 })
    expect(dialog.querySelector('code')?.textContent).toBe(text)
    if (name.endsWith('.mmd')) {
      document.documentElement.style.setProperty('--chat-code-keyword', '#123456')
      expect(getComputedStyle(dialog.querySelector('.hljs-keyword')!).color).toBe('rgb(18, 52, 86)')
    }
    await userEvent.click(within(dialog).getByRole('button', { name: 'Rendered' }))
    expect(within(dialog).getByRole('button', { name: 'Raw' })).toBeTruthy()
    if (view === 'markdown') expect(within(dialog).getByRole('heading', { name: 'Hello' })).toBeTruthy()
    if (view === 'html') expect(within(dialog).getByTitle(name)).toBeTruthy()
    if (view === 'csv') expect(within(dialog).getByRole('table')).toBeTruthy()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })
}

for (const width of [390, 3840]) for (const kind of ['workspace', 'published']) {
  it(`keeps a large ${kind} Raw diff plain and complete at ${width}`, async () => {
    await page.viewport(width, 900)
    const name = 'large.patch'
    const text = 'diff --git a/big.txt b/big.txt\n--- /dev/null\n+++ b/big.txt\n@@ -0,0 +1,150000 @@\n' + '+hello world\n'.repeat(150_000)
    const files: TransportFiles = {
      list: async () => [], raw: async () => new Blob([text]), download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') },
      workspaceFile: async () => ({ path: name, mtime: '', view: 'diff', content_type: 'text/plain', size: text.length, blob: new Blob([text]) }),
    }
    const props = { sessionId: 'large-source', files, onClose: () => {} }
    render(kind === 'workspace' ? <WorkspaceFileViewer {...props} path={name} /> : <ArtifactViewer {...props} artifact={{ id: name, name, view: 'diff', content_type: 'text/plain', size: text.length }} />)
    const dialog = screen.getByRole('dialog', { name })
    await userEvent.click(await within(dialog).findByRole('button', { name: 'Raw' }))
    const code = dialog.querySelector('[data-testid="workspace-code"] code')!
    // Structural budget: deterministic, unlike a wall-clock assertion on a shared test host.
    expect(code.querySelectorAll('span').length).toBe(0)
    expect(code.textContent).toBe(text)
    expect(within(dialog).getByText('Large diff shown as plain text to keep scrolling responsive.')).toBeTruthy()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    await userEvent.click(within(dialog).getByRole('button', { name: 'Rendered' }))
    expect(dialog.querySelectorAll('tr.diff-row').length).toBeGreaterThan(0)
    expect(dialog.querySelectorAll('tr.diff-row').length).toBeLessThanOrEqual(5000)
  })
}
