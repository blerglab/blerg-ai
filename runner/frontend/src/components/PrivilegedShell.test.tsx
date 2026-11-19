import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import HostShellConsentDialog from './HostShellConsentDialog'
import { tabLabel, type ShellTabView } from '../hooks/useSessionTabs'

it('keeps the privileged warning on a renamed tab', () => {
  const view: ShellTabView = { kind: 'shell', shellId: 'one', label: 'Maintenance', runtime: 'host', folder: 'project', privileged: true }
  expect(tabLabel(view)).toBe('Maintenance · Privileged · Not frozen at turn end')
  expect(tabLabel({ ...view, privileged: false })).toBe('Maintenance')
})

it('explains privileged sync and root access in consent', () => {
  render(<HostShellConsentDialog privileged challenge={{ challenge_id: 'one', machine_id: 'machine', account: 'person', uid: 1000, shell: '/bin/bash', cwd: '/project' }} busy={false} onAllow={() => {}} onCancel={() => {}} />)
  expect(screen.getByText(/This is a privileged shell/).textContent).toContain('not frozen at turn end')
  expect(screen.getByText(/This is a privileged shell/).textContent).toContain('may include in-flight writes')
})
