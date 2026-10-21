import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import LaunchPermissions from './LaunchPermissions'
import { NO_LAUNCH_PERMISSIONS } from '../lib/launchPermissions'

const open = (props: Partial<Parameters<typeof LaunchPermissions>[0]> = {}) => {
  const onChange = vi.fn()
  render(<LaunchPermissions kind="agent" runtime="docker" boards={[]} mcp={[]} connectionName={id => id} engines={['claude']}
    value={NO_LAUNCH_PERMISSIONS} onChange={onChange} {...props} />)
  fireEvent.click(screen.getByRole('button', { name: /Permissions/ }))
  return { dialog: screen.getByRole('dialog', { name: 'Permissions' }), onChange }
}

describe('the launch sheet children permissions', () => {
  it('offers an unsandboxed desktop session cluster children where the install has a cluster, with the cluster engines', async () => {
    const { dialog, onChange } = open({ runtime: 'daemon', cluster: { engines: ['codex'] } })
    const box = within(dialog).getByRole('checkbox', { name: 'Start children on the cluster without asking' })
    expect(within(dialog).getByTestId('pm-children-cluster')).toHaveTextContent('On the cluster.')
    expect(within(dialog).getByTestId('pm-children-cluster')).toHaveTextContent('Codex')
    expect(within(dialog).getByTestId('pm-children-docker')).toHaveTextContent('On this desktop.')
    fireEvent.click(box)
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(onChange).toHaveBeenCalledWith({ children: ['cluster'], board_levels: [], mcp_tools: [] }))
  })

  it('shows a sandboxed session the cluster row disabled, with why', () => {
    const { dialog } = open({ cluster: { engines: ['codex'] } })
    expect(within(dialog).getByRole('checkbox', { name: 'Start children on the cluster without asking' })).toBeDisabled()
    expect(within(dialog).getByTestId('pm-children-cluster')).toHaveTextContent('asks you for each cluster child, because the pod gets your credentials')
  })

  it('has no cluster row on a desktop-only install', () => {
    const { dialog } = open()
    expect(within(dialog).getByRole('checkbox', { name: 'Start sandboxed children without asking' })).toBeInTheDocument()
    expect(within(dialog).queryByRole('checkbox', { name: 'Start children on the cluster without asking' })).toBeNull()
  })
})
