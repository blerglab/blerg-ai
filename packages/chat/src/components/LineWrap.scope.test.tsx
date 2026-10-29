// The line wrap choice (#265) belongs to the signed-in account: through the real proxy transport
// (the Board's), account A's choice is not account B's, and without an account nothing is stored.
import { afterEach, describe, expect, it } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { createProxyTransport } from '../transport/proxy'
import ArtifactViewer from './ArtifactViewer'
import CodeViewer from './CodeViewer'
import { WrapScopeContext, resetLineWrap } from '../model/lineWrap'

afterEach(() => { resetLineWrap(); localStorage.clear() })

const code = { id: 'f1', name: 'main.ts', size: 30, content_type: 'text/plain', view: 'text' as const }
const fetcher: typeof fetch = async input => String(input).endsWith('/raw')
  ? new Response('export const a = 1\n', { headers: { 'Content-Length': '19' } })
  : new Response(JSON.stringify({ artifacts: [] }), { headers: { 'Content-Type': 'application/json' } })

async function wrapButton(files: NonNullable<ReturnType<typeof createProxyTransport>['files']>) {
  const view = render(<ArtifactViewer sessionId="s1" files={files} artifact={code} onClose={() => {}} />)
  const button = await screen.findByRole('button', { name: 'Wrap' })
  return { view, button }
}

describe('line wrap belongs to the account', () => {
  it('keeps account A’s choice from account B through the proxy transport', async () => {
    let subject = 'person-a'
    const files = createProxyTransport({ baseUrl: '/api/chat', fetch: fetcher, attachmentScope: () => subject }).files!
    let { view, button } = await wrapButton(files)
    expect(button).toHaveAttribute('aria-pressed', 'false')
    fireEvent.click(button)
    expect(button).toHaveAttribute('aria-pressed', 'true')
    view.unmount()
    subject = 'person-b'
    ;({ view, button } = await wrapButton(files))
    expect(button).toHaveAttribute('aria-pressed', 'false')
    view.unmount()
    subject = 'person-a'
    ;({ view, button } = await wrapButton(files))
    expect(button).toHaveAttribute('aria-pressed', 'true')
    view.unmount()
  })

  it('stores nothing on the device without an account', async () => {
    const files = createProxyTransport({ baseUrl: '/api/chat', fetch: fetcher }).files!
    const { button } = await wrapButton(files)
    fireEvent.click(button)
    expect(button).toHaveAttribute('aria-pressed', 'true')
    expect(Object.keys(localStorage).filter(key => key.startsWith('blerg.chat.wrap'))).toEqual([])
  })
})

describe('a published plain text file', () => {
  it('has the toggle and Alt+Z, wraps as prose by default and keeps the choice', async () => {
    const text = { id: 't1', name: 'build.txt', size: 12, content_type: 'text/plain', view: 'text' as const }
    const files = createProxyTransport({ baseUrl: '/api/chat', fetch: fetcher, attachmentScope: () => 'person-a' }).files!
    const view = render(<ArtifactViewer sessionId="s1" files={files} artifact={text} onClose={() => {}} />)
    const button = await screen.findByRole('button', { name: 'Wrap' })
    const box = () => screen.getByTestId('artifact-text').closest('[data-wrap]')!
    expect(box()).toHaveAttribute('data-wrap', 'on')
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'z', code: 'KeyZ', altKey: true })
    expect(box()).toHaveAttribute('data-wrap', 'off')
    expect(button).toHaveAttribute('aria-pressed', 'false')
    view.unmount()
    render(<ArtifactViewer sessionId="s1" files={files} artifact={text} onClose={() => {}} />)
    await screen.findByRole('button', { name: 'Wrap' })
    expect(box()).toHaveAttribute('data-wrap', 'off')
  })
})

describe('an expanded code block', () => {
  it('has the toggle and Alt+Z, and follows the account’s code choice', () => {
    const view = render(<WrapScopeContext.Provider value="person-a"><CodeViewer text={'const a = 1\n'} lang="javascript" onClose={() => {}} /></WrapScopeContext.Provider>)
    const button = screen.getByRole('button', { name: 'Wrap' })
    const box = () => screen.getByTestId('code-viewer-text')
    expect(button).toHaveAttribute('aria-pressed', 'false')
    expect(box()).toHaveAttribute('data-wrap', 'off')
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'z', code: 'KeyZ', altKey: true })
    expect(box()).toHaveAttribute('data-wrap', 'on')
    fireEvent.click(button)
    expect(box()).toHaveAttribute('data-wrap', 'off')
    fireEvent.click(button)
    view.unmount()
    render(<WrapScopeContext.Provider value="person-a"><CodeViewer text={'const b = 2\n'} lang="javascript" onClose={() => {}} /></WrapScopeContext.Provider>)
    expect(screen.getByTestId('code-viewer-text')).toHaveAttribute('data-wrap', 'on')
  })

  it('wraps pasted text (prose) by default', () => {
    render(<CodeViewer text={'some words\n'} lang={null} title="Message" onClose={() => {}} />)
    expect(screen.getByTestId('code-viewer-text')).toHaveAttribute('data-wrap', 'on')
  })
})

describe('a file over 5,000 lines', () => {
  const long = Array.from({ length: 5001 }, (_, i) => `line ${i + 1}`).join('\n') + '\n'

  it('does not wrap a published text file: the toggle is disabled with its reason, Alt+Z leaves it, the saved choice does not apply', async () => {
    const text = { id: 't2', name: 'big.txt', size: long.length, content_type: 'text/plain', view: 'text' as const }
    const fetchLong: typeof fetch = async input => String(input).endsWith('/raw')
      ? new Response(long, { headers: { 'Content-Length': String(long.length) } })
      : new Response(JSON.stringify({ artifacts: [] }), { headers: { 'Content-Type': 'application/json' } })
    const files = createProxyTransport({ baseUrl: '/api/chat', fetch: fetchLong, attachmentScope: () => 'person-a' }).files!
    render(<ArtifactViewer sessionId="s1" files={files} artifact={text} onClose={() => {}} />)
    const button = await screen.findByRole('button', { name: 'Wrap' })
    const box = () => screen.getByTestId('artifact-text').closest('[data-wrap]')!
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('title', 'Wrap is off for files over 5,000 lines')
    expect(button).toHaveAttribute('aria-pressed', 'false')
    expect(box()).toHaveAttribute('data-wrap', 'off')
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'z', code: 'KeyZ', altKey: true })
    expect(box()).toHaveAttribute('data-wrap', 'off')
  })

  it('does not wrap an expanded code block either', () => {
    render(<CodeViewer text={long} lang={null} title="Message" onClose={() => {}} />)
    expect(screen.getByRole('button', { name: 'Wrap' })).toBeDisabled()
    expect(screen.getByTestId('code-viewer-text')).toHaveAttribute('data-wrap', 'off')
  })
})
