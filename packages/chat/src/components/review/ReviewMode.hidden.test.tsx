// Review mode renders a document's HTML, so it says when the rendered page does not show every word
// of the source (an HTML comment, a closed <details>, text the sanitizer dropped): the person must
// never approve what they could not see. "Show source" opens the source beside the page.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import ReviewMode from './ReviewMode'
import { ChatContext, type ChatContextValue } from '../context'
import type { ArtifactInfo } from '../../transport/types'

const value: ChatContextValue = {
  sessionId: 's1', files: null, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined,
  submitFeedback: async () => true, reviewFor: async () => null,
}

function show(text: string) {
  const artifact: ArtifactInfo = { id: 'h1', name: 'plan.md', size: text.length, content_type: 'text/markdown', view: 'markdown', version: 1, latest_version: 1 }
  return render(<ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={text} onClose={() => {}} /></ChatContext.Provider>)
}

describe('review mode and hidden content', () => {
  it.each([
    ['an HTML comment', 'Ship it.\n\n<!-- also delete the backups -->\n'],
    ['a closed details block', 'Ship it.\n\n<details><summary>Notes</summary>also delete the backups</details>\n'],
    ['a dropped element', 'Ship it.\n\n<p>ok<title>also delete the backups</title></p>\n'],
  ])('says so for %s, and Show source opens the source', async (_name, text) => {
    show(text)
    const banner = await screen.findByTestId('review-hidden-content')
    expect(banner).toHaveTextContent('This document has hidden content')
    fireEvent.click(screen.getByRole('button', { name: 'Show source' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Document' })).toHaveAttribute('aria-pressed', 'true'))
    expect((screen.getByRole('textbox', { name: 'Source' }) as HTMLTextAreaElement).value).toContain('also delete the backups')
  })

  it('stays quiet when every word shows', async () => {
    show('Ship it.\n\n<p align="center"><b>All of it</b> shows.</p>\n\n<details open><summary>Notes</summary>open notes</details>\n')
    await screen.findByText('All of it')
    expect(screen.queryByTestId('review-hidden-content')).toBeNull()
  })
})
