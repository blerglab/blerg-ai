import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MessageText } from './AttachmentChips'
import { ChatContext, type ChatContextValue } from './context'
import { composeMessage } from '../model/attachments'
import { attachmentAt } from '../model/artifacts'
import type { ArtifactInfo } from '../transport/types'

vi.mock('./ArtifactCard', async importOriginal => ({ ...await importOriginal<typeof import('./ArtifactCard')>(), MediaPreview: ({ artifact }: { artifact: ArtifactInfo }) => <span data-testid="identity-preview">{artifact.id}</span> }))

const image = (id: string, size: number): ArtifactInfo => ({ id, name: 'image.png', size, origin: 'user', content_type: 'image/png', view: 'image' })

describe('attachment identities in a message', () => {
  it('previews both same-name files and keeps earlier pictures after a later upload', () => {
    let items = [image('first-image', 100), image('second-image', 200)]
    const text = composeMessage('Compare', items)
    const ctx = { sessionId: 'fixture', files: {}, view: vi.fn(), byName: () => items.at(-1), attachmentById: (id: string) => items.find(a => a.id === id) } as unknown as ChatContextValue
    const { rerender } = render(<ChatContext.Provider value={ctx}><MessageText text={text} /></ChatContext.Provider>)
    expect(screen.getAllByTestId('identity-preview').map(x => x.textContent)).toEqual(['first-image', 'second-image'])
    items = [...items, image('later-image', 300)]
    rerender(<ChatContext.Provider value={{ ...ctx }}><MessageText text={text} /></ChatContext.Provider>)
    expect(screen.getAllByTestId('identity-preview').map(x => x.textContent)).toEqual(['first-image', 'second-image'])
  })
  it('previews a legacy name-only note with the newest same-name upload at or before the message', () => {
    const uploads = [
      { ...image('before-older', 100), created_at: '2026-07-26T00:00:00Z' },
      { ...image('before-newest', 100), created_at: '2026-07-26T00:05:00Z' },
      { ...image('later-image', 100), created_at: '2026-07-26T00:10:00Z' },
    ]
    const ctx = { sessionId: 'fixture', files: {}, view: vi.fn(), byName: () => uploads[2], attachmentAt: (name: string, at: string) => attachmentAt(uploads, name, at) } as unknown as ChatContextValue
    const legacy = 'Look\n\nAttached files (fetch them with: blerg-runner fetch --all): image.png (100 B)'
    render(<ChatContext.Provider value={ctx}><MessageText text={legacy} at="2026-07-26T00:06:00Z" /></ChatContext.Provider>)
    expect(screen.getAllByTestId('identity-preview').map(x => x.textContent)).toEqual(['before-newest'])
    expect(screen.getByTestId('message-attachments')).toHaveTextContent('image.png')
  })
  it('never previews a legacy note with an upload made after the message', () => {
    const uploads = [{ ...image('later-image', 100), created_at: '2026-07-26T00:10:00Z' }]
    const ctx = { sessionId: 'fixture', files: {}, view: vi.fn(), byName: () => uploads[0], attachmentAt: (name: string, at: string) => attachmentAt(uploads, name, at) } as unknown as ChatContextValue
    const legacy = 'Look\n\nAttached files (fetch them with: blerg-runner fetch --all): image.png (100 B)'
    render(<ChatContext.Provider value={ctx}><MessageText text={legacy} at="2026-07-26T00:06:00Z" /></ChatContext.Provider>)
    expect(screen.queryByTestId('identity-preview')).toBeNull()
    expect(screen.getByTestId('message-attachments')).toHaveTextContent('image.png')
  })
  it('does not guess a legacy preview without the message time', () => {
    const uploads = [{ ...image('some-image', 100), created_at: '2026-07-26T00:00:00Z' }]
    const ctx = { sessionId: 'fixture', files: {}, view: vi.fn(), byName: () => uploads[0], attachmentAt: (name: string, at: string) => attachmentAt(uploads, name, at) } as unknown as ChatContextValue
    render(<ChatContext.Provider value={ctx}><MessageText text={'Look\n\nAttached files (fetch them with: blerg-runner fetch --all): image.png (100 B)'} /></ChatContext.Provider>)
    expect(screen.queryByTestId('identity-preview')).toBeNull()
  })
})
