import { fireEvent, render, screen, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { CodeActions, ExportTable } from './ContentActions'
import { downloadText } from '../model/contentExport'

vi.mock('../model/contentExport', async original => ({ ...await original<typeof import('../model/contentExport')>(), downloadText: vi.fn() }))

it('mounts only labeled triggers until inline table actions are first opened', () => {
  render(<>{Array.from({ length: 300 }, (_, i) => <ExportTable key={i}><tbody><tr><td>Table {i}</td></tr></tbody></ExportTable>)}</>)
  const triggers = screen.getAllByRole('button', { name: 'Table actions' })
  expect(triggers).toHaveLength(300)
  expect(document.querySelectorAll('svg')).toHaveLength(0)
  expect(screen.queryByRole('button', { name: 'Print' })).toBeNull()
  triggers[0].focus()
  fireEvent.click(triggers[0])
  const toolbar = screen.getByRole('toolbar', { name: 'Table export actions' })
  expect(within(toolbar).getByRole('button', { name: 'Download CSV' })).toBeVisible()
  expect(triggers[0]).toHaveAttribute('aria-expanded', 'true')
  const escape = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
  fireEvent(within(toolbar).getByRole('button', { name: 'Download CSV' }), escape)
  expect(escape.defaultPrevented).toBe(true)
  expect(triggers[0]).toHaveFocus()
  expect(triggers[0]).toHaveAttribute('aria-expanded', 'false')
  expect(screen.queryByRole('button', { name: 'Download CSV' })).toBeNull()
  expect(document.querySelectorAll('svg')).toHaveLength(3)
})

it('keeps an opened code export bound to the current content and closes on an outside pointer', async () => {
  const content = document.createElement('div')
  const view = render(<CodeActions inline text="old content" lang="typescript" content={() => content} />)
  const trigger = screen.getByRole('button', { name: 'Code actions' })
  expect(screen.queryByRole('button', { name: 'Download' })).toBeNull()
  fireEvent.click(trigger)
  view.rerender(<CodeActions inline text="current content" lang="python" content={() => content} />)
  fireEvent.click(screen.getByRole('button', { name: 'Download' }))
  await Promise.resolve()
  expect(downloadText).toHaveBeenCalledWith('current content', 'code.py', 'text/plain')
  fireEvent.pointerDown(document.body)
  expect(trigger).toHaveAttribute('aria-expanded', 'false')
  expect(screen.queryByRole('button', { name: 'Download' })).toBeNull()
})
