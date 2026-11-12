import ViewerToolbar from './ViewerToolbar'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import Markdown from './Markdown'
import MessageViewer from './MessageViewer'

it('offers table CSV, Markdown and print alongside the displayed cells', () => {
  render(<Markdown text={'| Name | Value |\n| --- | --- |\n| **Total** | =1+2 |'} />)
  const table = screen.getByRole('table').parentElement!
  fireEvent.click(within(table).getByRole('button', { name: 'Table actions' }))
  expect(within(table).getByRole('button', { name: 'Download CSV' })).toHaveAttribute('title', 'Download CSV')
  expect(within(table).getByRole('button', { name: 'Download CSV' })).toHaveTextContent('CSV')
  expect(within(table).getByRole('button', { name: 'Download Markdown' })).toHaveTextContent('MD')
  expect(within(table).getByRole('button', { name: 'Download Markdown' })).toHaveAttribute('title', 'Download Markdown')
  expect(within(table).getByRole('button', { name: 'Print' })).toBeInTheDocument()
})
it('offers print and Markdown download in a popped-out message toolbar', () => {
  render(<MessageViewer text="# Report" label="Report" embedded onClose={() => {}} />)
  const toolbar = screen.getByRole('toolbar')
  expect(within(toolbar).getByRole('button', { name: 'Print' })).toBeInTheDocument()
  expect(within(toolbar).getByRole('button', { name: 'Download Markdown' })).toBeInTheDocument()
})

it('reports unavailable print content with a safe cause', async () => {
  const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
  try {
    render(<ViewerToolbar titleId="empty" title="Empty" meta="Text" />)
    fireEvent.click(screen.getByRole('button', { name: 'Print' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not print this content. Try again.')
    expect(warn).toHaveBeenCalledWith('content_print_failed', { cause: 'browser_print_rejected' })
  } finally { warn.mockRestore() }
})
