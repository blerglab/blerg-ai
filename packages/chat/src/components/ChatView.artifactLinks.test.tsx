import { beforeEach, describe, expect, it } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { ev, meta, resetChatState, seed, SESSION } from '../test/chat'
import type { ArtifactInfo } from '../transport/types'

const report: ArtifactInfo = { id: 'a1', name: 'report.md', size: 20, content_type: 'text/markdown', view: 'markdown' }
const canonical = 'https://runner.example.com/sessions/s1'

beforeEach(resetChatState)

async function mountLink(href: string, opts: { canonical?: string | null; known?: boolean; files?: boolean; view?: string } = {}) {
  seed(ev({ kind: 'assistant_text', payload: { text: `[Published report](${href})` }, seq: 1 }))
  const t = createFakeTransport({ files: opts.files === false ? false : { items: opts.known === false ? [] : [{ ...report, view: opts.view ?? report.view }] } })
  t.files?.serve('# Published report contents')
  Object.assign(t.transport, { publishedSessionUrl: () => opts.canonical === undefined ? canonical : opts.canonical })
  render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
  await waitFor(() => expect(t.files?.listCalls ?? 1).toBeGreaterThan(0))
  return { ...t, link: await screen.findByRole('link', { name: 'Published report' }) }
}

describe('published artifact links', () => {
  it.each([`${canonical}?artifact=a1`, '/sessions/s1?artifact=a1', '?artifact=a1'])(
    'opens an existing artifact in the viewer for %s', async href => {
      const { link, files } = await mountLink(href)
      expect(fireEvent.click(link)).toBe(false)
      const dialog = await screen.findByRole('dialog', { name: /report\.md/ })
      expect(await within(dialog).findByRole('heading', { name: 'Published report contents' })).toBeInTheDocument()
      expect(files?.rawCalls).toEqual(['a1'])
    },
  )

  it.each([
    { ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { altKey: true }, { button: 1 }, { button: 2 },
  ])('preserves native navigation for a modified/nonprimary click %j', async options => {
    const { link, files } = await mountLink(`${canonical}?artifact=a1`)
    expect(fireEvent.click(link, options)).toBe(true)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(files?.rawCalls).toEqual([])
    expect(link).toHaveAttribute('href', `${canonical}?artifact=a1`)
    expect(link).toHaveAttribute('target', '_blank')
  })

  it.each([
    'https://other.example.com/sessions/s1?artifact=a1',
    'https://runner.example.com.evil.example/sessions/s1?artifact=a1',
    'https://runner.example.com@evil.example/sessions/s1?artifact=a1',
    'https://evil.example@runner.example.com/sessions/s1?artifact=a1',
    'http://runner.example.com/sessions/s1?artifact=a1',
    'https://runner.example.com:444/sessions/s1?artifact=a1',
    'https://runner.example.com/sessions/s2?artifact=a1',
    'https://runner.example.com/sessions/s1/other?artifact=a1',
    'https://runner.example.com/sessions/s1?artifact=missing',
    'https://runner.example.com/sessions/s1?artifact=a1&artifact=other',
    'https://runner.example.com/sessions/s1?artifact=a1&next=other',
    'https://runner.example.com/sessions/s1#artifact=a1',
    'https://runner.example.com/docs?artifact=a1',
  ])('does not capture unrelated or spoofed links: %s', async href => {
    const { link, files } = await mountLink(href)
    expect(fireEvent.click(link)).toBe(true)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(files?.rawCalls).toEqual([])
    expect(link).toHaveAttribute('href', href)
  })

  it.each([
    { canonical: null }, { canonical: 'https://runner.example.com/sessions/s1?next=x' },
    { canonical: 'https://user@runner.example.com/sessions/s1' },
    { canonical: 'https://runner.example.com/sessions/s1#part' },
    { known: false }, { files: false }, { view: 'none' },
  ])('requires trusted canonical context and a known viewable file: %j', async options => {
    const { link } = await mountLink(`${canonical}?artifact=a1`, options)
    expect(fireEvent.click(link)).toBe(true)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('uses the declared runner URL rather than the embedding page origin for relative navigation', async () => {
    const { link } = await mountLink('?artifact=a1')
    expect(link).toHaveAttribute('href', `${canonical}?artifact=a1`)
    expect(fireEvent.click(link, { metaKey: true })).toBe(true)
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})
