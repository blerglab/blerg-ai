import { beforeEach, expect, it } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import WorkspaceLayouts from './WorkspaceLayouts'
import { useSessionTabs } from '../hooks/useSessionTabs'
import { useToastStore } from '../hooks/useToastStore'
function Harness() { const tabs = useSessionTabs('owner', 'workspace', false); return <WorkspaceLayouts account="owner" tabs={tabs} /> }
beforeEach(() => { localStorage.clear(); useToastStore.setState({ toasts: [] }) })
it('Undo restores a deleted layout without removing layouts saved since the deletion', () => {
  render(<Harness />)
  fireEvent.click(screen.getByRole('button', { name: 'Layouts' }))
  const save = (name: string) => { fireEvent.change(screen.getByRole('textbox', { name: 'Layout name' }), { target: { value: name } }); fireEvent.click(screen.getByRole('button', { name: 'Save' })) }
  save('Review')
  fireEvent.click(screen.getByRole('button', { name: 'Delete Review' }))
  const undo = useToastStore.getState().toasts.at(-1)!.action!.onClick
  save('Planning')
  act(undo)
  expect(screen.getByRole('button', { name: 'Load Review' })).toBeVisible()
  expect(screen.getByRole('button', { name: 'Load Planning' })).toBeVisible()
})

it('welcomes an empty library and shows a new visual card with a summary, time and confirmation', () => {
  render(<Harness />)
  fireEvent.click(screen.getByRole('button', { name: 'Layouts' }))
  expect(screen.getByText(/Save an arrangement you like/)).toBeVisible()
  fireEvent.change(screen.getByRole('textbox', { name: 'Layout name' }), { target: { value: 'Deep work' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  const card = screen.getByRole('article', { name: 'Deep work' })
  expect(card).toHaveTextContent('Main')
  expect(card.querySelector('svg')).not.toBeNull()
  expect(card.querySelector('time')).not.toBeNull()
  expect(screen.getByRole('status')).toHaveTextContent('Saved “Deep work”')
  expect(screen.getByRole('article', { name: 'Recover last layout' })).toBeVisible()
})
