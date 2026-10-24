import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { ChatView, type WorkspaceFile } from '@blerglab/chat'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'
import { ev, meta, resetChatState, seed, SESSION } from '../../../../packages/chat/src/test/chat'
import '../index.css'
import '@blerglab/chat/blerg.css'

afterEach(async () => { await page.viewport(1280, 900) })

describe('project README references from chat', () => {
  it.each([390, 3840])('opens a project reference in the viewer at %d px', async width => {
    await page.viewport(width, 1000)
    resetChatState()
    const path = '/workspace/example-app/README.md'
    seed(
      ev({ kind: 'user_message', payload: { text: 'Show me the readme', source: 'user' }, seq: 1 }),
      ev({ kind: 'assistant_text', payload: { text: `[Project README](${path})` }, seq: 2 }),
    )
    const fake = createFakeTransport()
    const read = vi.fn(async (_session: string, requested: string): Promise<WorkspaceFile> => {
      // Model the route's normalized response: a project file, not an output artifact.
      if (requested !== path) throw new Error('wrong project path')
      const blob = new Blob(['# Example project\n\nThis README belongs to the authorized project.'])
      return { path: 'README.md', size: blob.size, view: 'markdown', content_type: 'text/markdown', mtime: '', blob }
    })
    Object.assign(fake.files!, { workspaceFile: read })
    render(<ChatView session={SESSION} transport={fake.transport} meta={meta()} canOpenWorkspaceFiles />)
    await userEvent.click(await screen.findByRole('button', { name: 'Project README' }))
    const dialog = await screen.findByRole('dialog', { name: path })
    expect(await within(dialog).findByRole('heading', { name: 'Example project', level: 1 })).toBeTruthy()
    expect(read).toHaveBeenCalledWith(SESSION, path, expect.anything())
    const rect = dialog.getBoundingClientRect()
    expect(rect.left).toBeGreaterThanOrEqual(0)
    expect(rect.right).toBeLessThanOrEqual(width)
    expect(within(dialog).getByRole('button', { name: 'Close' }).getBoundingClientRect().right).toBeLessThanOrEqual(width)
  })
})
