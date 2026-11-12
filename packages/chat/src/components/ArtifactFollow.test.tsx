import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { localStorageChatStorage } from '../model/storage'
import ArtifactViewer from './ArtifactViewer'
import { createFakeFiles } from '../test/fakeTransport'
import type { ArtifactInfo } from '../transport/types'

const first: ArtifactInfo = { id: 'live-one', name: 'viz.html', view: 'html', content_type: 'text/html', size: 20, version: 1 }
const second = { ...first, id: 'live-two', version: 2 }
function fixture() {
  localStorageChatStorage.set('blerg.viewer.follow.follow-test.agent.viz.html', '0')
  localStorageChatStorage.set('blerg.viewer.follow.follow-test.agent.plot.vl.json', '0')
  const files = createFakeFiles()
  files.raw = vi.fn(async (_s: string, id: string) => new Blob([`<p>${id}</p>`]))
  const props = { sessionId: 'follow-test', files, artifact: first, latest: second, embedded: true, onClose: vi.fn() }
  return { files, props }
}
const frame = (container: HTMLElement) => container.querySelector('iframe')!

describe('published preview following', () => {
  it('starts pinned, explicitly follows, and pins the displayed version', async () => {
    const { props } = fixture()
    const view = render(<ArtifactViewer {...props} />)
    await waitFor(() => expect(frame(view.container)?.srcdoc).toContain('live-one'))
    fireEvent.click(screen.getByRole('button', { name: 'Follow latest' }))
    await waitFor(() => expect(frame(view.container)?.srcdoc).toContain('live-two'))
    fireEvent.click(screen.getByRole('button', { name: 'Pin version' }))
    view.rerender(<ArtifactViewer {...props} latest={{ ...second, id: 'live-three', version: 3 }} />)
    expect(frame(view.container).srcdoc).toContain('live-two')
    expect(screen.getByRole('button', { name: 'Follow latest' })).toBeTruthy()
  })
  it('keeps the last preview on a failed read and reports the failure', async () => {
    const { props, files } = fixture()
    files.raw = vi.fn(async (_s, id) => { if (id === second.id) throw new Error('private details'); return new Blob(['<p>last good</p>']) })
    const view = render(<ArtifactViewer {...props} />)
    await waitFor(() => expect(frame(view.container)?.srcdoc).toContain('last good'))
    fireEvent.click(screen.getByRole('button', { name: 'Follow latest' }))
    await screen.findByText('Could not update preview. The previous version is still shown.')
    expect(frame(view.container).srcdoc).toContain('last good')
    expect(view.container.textContent).not.toContain('private details')
  })
  it('does not follow a different name, origin, or preview type', async () => {
    const { props, files } = fixture()
    const view = render(<ArtifactViewer {...props} latest={{ ...second, name: 'other.html' }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Follow latest' }))
    for (const latest of [{ ...second, origin: 'user' as const }, { ...second, view: 'pdf' }]) {
      view.rerender(<ArtifactViewer {...props} latest={latest} />)
    }
    await act(() => new Promise(resolve => setTimeout(resolve, 400)))
    expect(files.raw).toHaveBeenCalledTimes(1)
  })
  it('ignores an in-flight read after pinning', async () => {
    const { props, files } = fixture()
    let finish: (blob: Blob) => void = () => {}
    files.raw = vi.fn(async (_s, id) => id === second.id ? new Promise<Blob>(resolve => { finish = resolve }) : new Blob(['<p>original</p>']))
    const view = render(<ArtifactViewer {...props} />)
    await waitFor(() => expect(frame(view.container)?.srcdoc).toContain('original'))
    fireEvent.click(screen.getByRole('button', { name: 'Follow latest' }))
    await waitFor(() => expect(files.raw).toHaveBeenCalledTimes(2))
    fireEvent.click(screen.getByRole('button', { name: 'Pin version' }))
    finish(new Blob(['<p>stale</p>']))
    await act(() => new Promise(resolve => setTimeout(resolve, 20)))
    expect(frame(view.container).srcdoc).toContain('original')
  })
  it('coalesces rapid publications and rejects oversized bytes', async () => {
    const { props, files } = fixture()
    files.raw = vi.fn(async (_s, id) => new Blob([id === 'huge' ? 'x'.repeat(2 * 1024 * 1024 + 1) : id]))
    const view = render(<ArtifactViewer {...props} />)
    await waitFor(() => expect(frame(view.container)?.srcdoc).toContain(first.id))
    fireEvent.click(screen.getByRole('button', { name: 'Follow latest' }))
    view.rerender(<ArtifactViewer {...props} latest={{ ...second, id: 'huge', version: 3 }} />)
    await screen.findByText('Could not update preview. The previous version is still shown.')
    expect(files.raw).toHaveBeenCalledTimes(2)
    expect(files.raw).not.toHaveBeenCalledWith(props.sessionId, second.id)
    expect(frame(view.container).srcdoc).toContain(first.id)
  })
  it('retains a chart when its replacement fails validation', async () => {
    const { props, files } = fixture()
    const artifact = { ...first, name: 'plot.vl.json', view: 'json', content_type: 'application/json' }
    const latest = { ...artifact, id: second.id, version: 2 }
    files.raw = vi.fn(async (_s, id) => new Blob([id === second.id ? '{broken' : '{"mark":"point","data":{"values":[{"x":1}]},"encoding":{"x":{"field":"x","type":"quantitative"}}}']))
    render(<ArtifactViewer {...props} artifact={artifact} latest={latest} />)
    fireEvent.click(screen.getByRole('button', { name: 'Follow latest' }))
    await screen.findByText('Could not update preview. The previous version is still shown.')
    expect(screen.getByRole('button', { name: 'Pin version' })).toHaveAttribute('aria-pressed', 'true')
  })

})

it('follows by default and remembers an explicit pin when reopened', async () => {
  const { props } = fixture()
  const key = 'blerg.viewer.follow.follow-test.agent.viz.html'
  localStorageChatStorage.set(key, null)
  const view = render(<ArtifactViewer {...props} latest={first} />)
  await waitFor(() => expect(frame(view.container)?.srcdoc).toContain(first.id))
  expect(screen.getByRole('button', { name: 'Pin version' })).toHaveAttribute('aria-pressed', 'true')
  view.rerender(<ArtifactViewer {...props} />)
  await waitFor(() => expect(frame(view.container)?.srcdoc).toContain(second.id))
  fireEvent.click(screen.getByRole('button', { name: 'Pin version' }))
  expect(localStorageChatStorage.get(key)).toBe('0')
  view.unmount()
  const reopened = render(<ArtifactViewer {...props} />)
  await waitFor(() => expect(frame(reopened.container)?.srcdoc).toContain(first.id))
  expect(screen.getByRole('button', { name: 'Follow latest' })).toHaveAttribute('aria-pressed', 'false')
  expect(screen.getByRole('button', { name: 'v2 is available' })).toHaveAttribute('title', 'v2 is available')
  fireEvent.click(screen.getByRole('button', { name: 'v2 is available' }))
  await waitFor(() => expect(frame(reopened.container)?.srcdoc).toContain(second.id))
  expect(screen.queryByRole('button', { name: 'v2 is available' })).toBeNull()
})

it('keeps a followed SVG in the safe data URL cache without reading it twice', async () => {
  const { props, files } = fixture()
  const artifact = { ...first, name: 'drawing.svg', view: 'image', content_type: 'image/svg+xml' }
  localStorageChatStorage.set('blerg.viewer.follow.follow-test.agent.drawing.svg', null)
  files.raw = vi.fn(async (_s, id) => new Blob([`<svg xmlns="http://www.w3.org/2000/svg"><title>${id}</title></svg>`]))
  const view = render(<ArtifactViewer {...props} artifact={artifact} latest={artifact} />)
  await waitFor(() => expect(view.container.querySelector('img')?.src).toContain('data:image/svg+xml'))
  view.rerender(<ArtifactViewer {...props} artifact={artifact} latest={{ ...artifact, id: second.id, version: 2 }} />)
  await waitFor(() => expect(screen.getByRole('region', { name: /drawing.svg · v2/ })).toBeTruthy())
  expect(files.raw).toHaveBeenCalledTimes(2)
})

it('offers a newer version while pinned even when its preview type changed', async () => {
  const { props } = fixture()
  render(<ArtifactViewer {...props} latest={{ ...second, view: 'markdown' }} />)
  expect(await screen.findByRole('button', { name: 'v2 is available' })).toBeTruthy()
})

it('names the shown version and opens an earlier one, which pins it', async () => {
  const { props, files } = fixture()
  const key = 'blerg.viewer.follow.follow-test.agent.viz.html'
  localStorageChatStorage.set(key, null)
  files.list = vi.fn(async () => [first, second])
  const view = render(<ArtifactViewer {...props} artifact={second} />)
  const picker = await screen.findByRole('combobox', { name: 'Version' })
  await screen.findByRole('option', { name: 'v1' })
  expect(picker).toHaveValue(second.id)
  expect(screen.getByRole('option', { name: 'v2 (latest)' })).toBeTruthy()
  fireEvent.change(picker, { target: { value: first.id } })
  await waitFor(() => expect(frame(view.container)?.srcdoc).toContain(first.id))
  expect(screen.getByRole('combobox', { name: 'Version' })).toHaveValue(first.id)
  expect(localStorageChatStorage.get(key)).toBe('0')
  expect(screen.getByRole('button', { name: 'Follow latest' })).toHaveAttribute('aria-pressed', 'false')
  const third = { ...first, id: 'live-three', version: 3 }
  files.list = vi.fn(async () => [first, second, third])
  view.rerender(<ArtifactViewer {...props} artifact={second} latest={third} />)
  expect(await screen.findByRole('option', { name: 'v3 (latest)' })).toBeTruthy()
  expect(screen.getByRole('option', { name: 'v2' })).toBeTruthy()
  expect(screen.getByRole('combobox', { name: 'Version' })).toHaveValue(first.id)
})

it('offers no version picker for a file with one version', async () => {
  const { props, files } = fixture()
  files.list = vi.fn(async () => [first])
  const view = render(<ArtifactViewer {...props} latest={first} />)
  await waitFor(() => expect(frame(view.container)?.srcdoc).toContain(first.id))
  await waitFor(() => expect(files.list).toHaveBeenCalled())
  expect(screen.queryByRole('combobox', { name: 'Version' })).toBeNull()
})
