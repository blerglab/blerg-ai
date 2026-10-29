import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import ViewerToolbar, { ViewerAction } from './ViewerToolbar'

afterEach(() => vi.unstubAllGlobals())
it('copies the full path from its truncated title or icon and labels icon actions', async () => {
  const writeText = vi.fn(async () => {})
  vi.stubGlobal('navigator', { clipboard: { writeText } })
  const download = vi.fn()
  render(<ViewerToolbar titleId="path" title="src/deep/main.go" meta="From the session’s folder · as of now · 27 KB">
    <ViewerAction icon="download" label="Download" onClick={download} />
  </ViewerToolbar>)
  fireEvent.click(screen.getByRole('button', { name: 'Copy src/deep/main.go path' }))
  expect(writeText).toHaveBeenLastCalledWith('src/deep/main.go')
  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Copied'))
  fireEvent.click(screen.getByRole('button', { name: 'Copy path' }))
  expect(writeText).toHaveBeenCalledTimes(2)
  fireEvent.click(screen.getByRole('button', { name: 'Download' }))
  expect(download).toHaveBeenCalledOnce()
})
