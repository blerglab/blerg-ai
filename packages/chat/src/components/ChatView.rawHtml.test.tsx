// A chat message shows every byte the agent wrote: raw HTML stays literal text (only a document
// surface renders it), so a comment or a closed <details> cannot hide words from the person.
import { beforeEach, describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { ev, meta, resetChatState, seed, SESSION } from '../test/chat'

beforeEach(resetChatState)

describe('raw HTML in a chat message', () => {
  it('stays literal: a comment and a details block show as written', async () => {
    seed(ev({ kind: 'assistant_text', payload: { text: 'Done.\n\n<!-- hidden -->\n\n<details><summary>More</summary>secret steps</details>' }, seq: 1 }))
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
    const done = await screen.findByText('Done.')
    const message = done.closest('.md')!
    expect(message.textContent).toContain('<!-- hidden -->')
    expect(message.textContent).toContain('<details><summary>More</summary>secret steps</details>')
    expect(message.querySelector('details')).toBeNull()
  })
})
