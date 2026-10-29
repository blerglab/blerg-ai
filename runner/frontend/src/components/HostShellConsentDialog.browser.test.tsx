import '../index.css'
import { render, screen, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { userEvent } from 'vitest/browser'
import HostShellConsentDialog from './HostShellConsentDialog'

const challenge = { challenge_id: 'c1', machine_id: 'machine', account: 'person', uid: 1000, shell: '/bin/bash', cwd: '/home/person/project' }

// The owner could not answer the consent dialog from the keyboard: arrows did nothing, and focus
// taken by something outside (a terminal tab) left Enter with no target.
it('moves between Cancel and Allow with the arrow keys and answers with Enter', async () => {
  const onAllow = vi.fn(), onCancel = vi.fn()
  render(<HostShellConsentDialog challenge={challenge} busy={false} onAllow={onAllow} onCancel={onCancel} />)
  const cancel = screen.getByRole('button', { name: 'Cancel' }), allow = screen.getByRole('button', { name: 'Allow this launch' })
  await waitFor(() => expect(cancel).toHaveFocus())
  await userEvent.keyboard('{ArrowRight}')
  expect(allow).toHaveFocus()
  await userEvent.keyboard('{ArrowRight}')
  expect(cancel).toHaveFocus()
  await userEvent.keyboard('{ArrowLeft}')
  expect(allow).toHaveFocus()
  await userEvent.keyboard('{Enter}')
  expect(onAllow).toHaveBeenCalledTimes(1)
  expect(onCancel).not.toHaveBeenCalled()
})

it('takes focus back when something outside grabs it while open', async () => {
  const outside = document.createElement('button'); outside.textContent = 'outside'
  document.body.prepend(outside)
  render(<HostShellConsentDialog challenge={challenge} busy={false} onAllow={() => {}} onCancel={() => {}} />)
  const cancel = screen.getByRole('button', { name: 'Cancel' })
  await waitFor(() => expect(cancel).toHaveFocus())
  outside.inert = false
  outside.focus()
  await waitFor(() => expect(cancel).toHaveFocus())
  outside.remove()
})

// The owner opened the dialog with a mouse click: Cancel had focus but no ring, since a browser
// does not count focus moved by script after a click as focus-visible.
it('shows where focus is when opened by a mouse click', async () => {
  const opener = document.createElement('button'); opener.textContent = 'Shell'
  document.body.append(opener)
  await userEvent.click(opener)
  render(<HostShellConsentDialog challenge={challenge} busy={false} onAllow={() => {}} onCancel={() => {}} />)
  const cancel = screen.getByRole('button', { name: 'Cancel' })
  await waitFor(() => expect(cancel).toHaveFocus())
  const style = getComputedStyle(cancel)
  expect(style.outlineStyle).not.toBe('none')
  expect(parseFloat(style.outlineWidth)).toBeGreaterThan(0)
  opener.remove()
})
