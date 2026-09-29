import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import ReposRootEditor from './ReposRootEditor'

function jsonResponse(status: number, body: unknown) {
  return Promise.resolve({ ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) })
}

function renderEditor(onChanged = vi.fn()) {
  render(<ReposRootEditor daemonId="d1" daemonName="workstation" reposRoot="/home/dev/repos" onChanged={onChanged} />)
  return onChanged
}

describe('ReposRootEditor', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('shows the current folder with a Change affordance', () => {
    renderEditor()
    expect(screen.getByTestId('repos-root-value')).toHaveTextContent('/home/dev/repos')
    expect(screen.getByRole('button', { name: "Change workstation's repos folder" })).toBeInTheDocument()
    expect(screen.queryByTestId('repos-root-input')).not.toBeInTheDocument()
  })

  it('submits the new folder to the daemon endpoint and confirms it applied', async () => {
    const fetchMock = vi.fn(() => jsonResponse(200, { repos_root: '/srv/repos' }))
    vi.stubGlobal('fetch', fetchMock)
    const onChanged = renderEditor()

    fireEvent.click(screen.getByTestId('repos-root-edit'))
    const input = screen.getByTestId('repos-root-input')
    expect(input).toHaveValue('/home/dev/repos')
    fireEvent.change(input, { target: { value: '  /srv/repos/  ' } })
    fireEvent.click(screen.getByTestId('repos-root-save'))

    await screen.findByTestId('repos-root-applied')
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/daemons/d1/repos-root')
    expect(init.method).toBe('PUT')
    expect(JSON.parse(init.body as string)).toEqual({ repos_root: '/srv/repos/' })
    // The daemon's cleaned value is what the sheet is told, not the draft.
    expect(onChanged).toHaveBeenCalledWith('/srv/repos')
    expect(screen.queryByTestId('repos-root-input')).not.toBeInTheDocument()
  })

  it("shows the daemon's refusal inline and keeps the form open", async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(422, { error: "the daemon can't write to /srv/ro: permission denied" })))
    const onChanged = renderEditor()

    fireEvent.click(screen.getByTestId('repos-root-edit'))
    fireEvent.change(screen.getByTestId('repos-root-input'), { target: { value: '/srv/ro' } })
    fireEvent.click(screen.getByTestId('repos-root-save'))

    const err = await screen.findByTestId('repos-root-error')
    expect(err).toHaveTextContent("the daemon can't write to /srv/ro: permission denied")
    expect(screen.getByTestId('repos-root-input')).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByTestId('repos-root-input')).toHaveValue('/srv/ro')
    expect(onChanged).not.toHaveBeenCalled()
    expect(screen.queryByTestId('repos-root-applied')).not.toBeInTheDocument()
  })

  it('reports a network failure without claiming success', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))))
    const onChanged = renderEditor()
    fireEvent.click(screen.getByTestId('repos-root-edit'))
    fireEvent.change(screen.getByTestId('repos-root-input'), { target: { value: '/srv/repos' } })
    fireEvent.click(screen.getByTestId('repos-root-save'))
    expect(await screen.findByTestId('repos-root-error')).toHaveTextContent('offline')
    expect(onChanged).not.toHaveBeenCalled()
  })

  it('does not submit an empty or unchanged folder', () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    renderEditor()
    fireEvent.click(screen.getByTestId('repos-root-edit'))
    expect(screen.getByTestId('repos-root-save')).toBeDisabled()
    fireEvent.change(screen.getByTestId('repos-root-input'), { target: { value: '   ' } })
    expect(screen.getByTestId('repos-root-save')).toBeDisabled()
    fireEvent.submit(screen.getByTestId('repos-root'))
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('Cancel and Escape leave the folder as it was', async () => {
    renderEditor()
    fireEvent.click(screen.getByTestId('repos-root-edit'))
    fireEvent.change(screen.getByTestId('repos-root-input'), { target: { value: '/elsewhere' } })
    fireEvent.click(screen.getByTestId('repos-root-cancel'))
    expect(screen.getByTestId('repos-root-value')).toHaveTextContent('/home/dev/repos')

    fireEvent.click(screen.getByTestId('repos-root-edit'))
    expect(screen.getByTestId('repos-root-input')).toHaveValue('/home/dev/repos')
    fireEvent.keyDown(screen.getByTestId('repos-root-input'), { key: 'Escape' })
    await waitFor(() => expect(screen.queryByTestId('repos-root-input')).not.toBeInTheDocument())
  })
})
