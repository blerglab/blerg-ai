import { afterEach, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import InitialLoader from './InitialLoader'

afterEach(() => { vi.useRealTimers() })

it.each(['rows', 'facts', 'terminal'] as const)('delays the %s skeleton for 150 ms and labels its busy region', async shape => {
  vi.useFakeTimers()
  const view = render(<InitialLoader loading label="workspace" shape={shape} />)
  expect(screen.queryByRole('region', { name: 'workspace' })).toBeNull()
  expect(view.container.querySelector('[aria-busy]')).toBeNull()
  expect(view.container.querySelector('.initial-skeleton')).toBeNull()
  await act(() => vi.advanceTimersByTimeAsync(149))
  expect(screen.queryByText('Loading workspace')).toBeNull()
  expect(screen.queryByRole('region')).toBeNull()
  await act(() => vi.advanceTimersByTimeAsync(1))
  expect(screen.getByRole('region', { name: 'workspace' })).toHaveAttribute('aria-busy', 'true')
  expect(screen.getByText('Loading workspace')).toHaveClass('initial-loader-label')
  expect(view.container.querySelector(`.initial-skeleton-${shape}`)).toHaveAttribute('aria-hidden', 'true')
  expect(view.container.querySelectorAll('button, input, [tabindex]')).toHaveLength(0)
})

it('never flashes for fast loads or cached content, including an empty cached result', async () => {
  vi.useFakeTimers()
  const view = render(<InitialLoader loading label="files"><p>Files</p></InitialLoader>)
  await act(() => vi.advanceTimersByTimeAsync(100))
  view.rerender(<InitialLoader loading={false} label="files"><p>No files</p></InitialLoader>)
  await act(() => vi.advanceTimersByTimeAsync(300))
  expect(screen.getByText('No files')).toBeInTheDocument()
  expect(view.container.querySelector('[aria-busy=true]')).toBeNull()
  view.rerender(<InitialLoader loading hasContent label="files"><p>Cached files</p></InitialLoader>)
  await act(() => vi.advanceTimersByTimeAsync(300))
  expect(screen.getByText('Cached files')).toBeInTheDocument()
  expect(view.container.querySelector('.initial-skeleton')).toBeNull()
})

it('starts a fresh delay when the load identity changes and cleans up on unmount', async () => {
  vi.useFakeTimers()
  const view = render(<InitialLoader loading loadKey="one" label="files" />)
  await act(() => vi.advanceTimersByTimeAsync(150))
  expect(screen.getByText('Loading files')).toBeInTheDocument()
  view.rerender(<InitialLoader loading loadKey="two" label="files" />)
  expect(screen.queryByText('Loading files')).toBeNull()
  await act(() => vi.advanceTimersByTimeAsync(149))
  expect(screen.queryByText('Loading files')).toBeNull()
  view.unmount()
  expect(vi.getTimerCount()).toBe(0)
})

it('does not start an initial loader while a surface is inactive', async () => {
  vi.useFakeTimers()
  const view = render(<InitialLoader loading active={false} label="history" />)
  await act(() => vi.advanceTimersByTimeAsync(300))
  expect(vi.getTimerCount()).toBe(0)
  expect(view.container).toBeEmptyDOMElement()
  view.rerender(<InitialLoader loading active label="history" />)
  await act(() => vi.advanceTimersByTimeAsync(149))
  expect(screen.queryByText('Loading history')).toBeNull()
  await act(() => vi.advanceTimersByTimeAsync(1))
  expect(screen.getByText('Loading history')).toBeInTheDocument()
})
