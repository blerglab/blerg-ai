import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { IconMenu } from './IconButton'
import { ParentDiscardAction } from './ChildDiscard'

vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))

describe('IconMenu', () => {
  it('opens on ArrowDown from its trigger and moves between items', () => {
    render(<IconMenu label="More actions"><button role="menuitem">One</button><button role="menuitem">Two</button></IconMenu>)
    const trigger = screen.getByRole('button', { name: 'More actions' })
    trigger.focus()
    fireEvent.keyDown(trigger, { key: 'ArrowDown' })
    expect(screen.getByRole('menu')).not.toHaveAttribute('hidden')
    expect(screen.getByRole('menuitem', { name: 'One' })).toHaveFocus()
    fireEvent.keyDown(screen.getByRole('menuitem', { name: 'One' }), { key: 'ArrowDown' })
    expect(screen.getByRole('menuitem', { name: 'Two' })).toHaveFocus()
  })

  it('leaves keys in a dialog an item opened to that dialog: an arrow neither reopens the menu nor moves focus', () => {
    render(<IconMenu label="More actions"><ParentDiscardAction sessionId="s" title="Work" menu hint="Stop and archive" /></IconMenu>)
    fireEvent.click(screen.getByRole('button', { name: 'More actions' }))
    fireEvent.click(screen.getByRole('menuitem', { name: 'Discard session and children' }))
    expect(screen.getByRole('menu', { hidden: true })).toHaveAttribute('hidden')
    const cancel = screen.getByRole('button', { name: 'Cancel' })
    cancel.focus()
    fireEvent.keyDown(cancel, { key: 'ArrowDown' })
    fireEvent.keyDown(cancel, { key: 'ArrowUp' })
    expect(screen.getByRole('menu', { hidden: true })).toHaveAttribute('hidden')
    expect(cancel).toHaveFocus()
    expect(screen.getByRole('dialog', { name: 'Discard session and all children?' })).toBeInTheDocument()
  })
})
