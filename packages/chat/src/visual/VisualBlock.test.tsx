import { StrictMode } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import VisualBlock from '../components/VisualBlock'
import { renderVisual } from './render'

vi.mock('./render', () => ({renderVisual: vi.fn()}))
const renderer = vi.mocked(renderVisual)
beforeEach(() => { renderer.mockReset() })

describe('visual block controls and lifetime', () => {
  it('shows source, copies original, opens a zoomable viewer', async () => {
    const copy = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {configurable: true, value: {writeText: copy}})
    renderer.mockImplementation(async (_kind, _source, host) => {
      host.textContent = 'Rendered diagram'
      return () => host.replaceChildren()
    })
    render(<VisualBlock kind="mermaid" source="flowchart LR; A-->B" />)
    expect(await screen.findByText('Rendered diagram')).toBeInTheDocument()
    await act(async () => {fireEvent.click(screen.getByRole('button', {name: 'Copy'}))})
    expect(copy).toHaveBeenCalledWith('flowchart LR; A-->B')
    fireEvent.click(screen.getByRole('button', {name: 'Source'}))
    expect(screen.getByText('flowchart LR; A-->B')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', {name: 'Open'}))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', {name: 'Zoom in'}))
    expect(screen.getByRole('button', {name: 'Reset zoom'})).toHaveTextContent('125%')
    fireEvent.keyDown(screen.getByRole('dialog'), {key: 'Escape'})
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
  it('falls back to source after runtime or parsing failure', async () => {
    renderer.mockRejectedValue(new Error('not a valid diagram'))
    render(<VisualBlock kind="mermaid" source="invalid source" />)
    expect(await screen.findByText('Preview unavailable; showing source.')).toBeInTheDocument()
    expect(screen.getByText('invalid source')).toBeInTheDocument()
  })
  it('isolates late StrictMode work from the current rendering and disposes both', async () => {
    const pending: {host: HTMLElement; resolve: (dispose: () => void) => void}[] = []
    renderer.mockImplementation((_kind, _source, host) => new Promise(resolve => pending.push({host, resolve})))
    const firstCleanup = vi.fn()
    const currentCleanup = vi.fn()
    const {unmount} = render(<StrictMode><VisualBlock kind="mermaid" source="flowchart LR; A-->B" /></StrictMode>)
    expect(pending).toHaveLength(2)
    await act(async () => {
      pending[1].host.textContent = 'current visual'
      pending[1].resolve(currentCleanup)
    })
    await act(async () => {
      pending[0].host.textContent = 'late stale visual'
      pending[0].resolve(firstCleanup)
    })
    expect(screen.getByText('current visual')).toBeInTheDocument()
    expect(screen.queryByText('late stale visual')).not.toBeInTheDocument()
    await waitFor(() => expect(firstCleanup).toHaveBeenCalledOnce())
    unmount()
    expect(currentCleanup).toHaveBeenCalledOnce()
  })
})
