import { render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import QuestionTabs from './QuestionTabs'
import { useSessionStore } from '../hooks/useSessionStore'
import type { SessionInfo } from '../types'
import type { SessionTabs } from '../hooks/useSessionTabs'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('@blerglab/chat', async original => ({
 ...await original<typeof import('@blerglab/chat')>(),
 QuestionPanel: ({ worker }: { worker?: string }) => <div>{worker}</div>,
}))

it('keeps the worker identity when opening its question tab from its own session', () => {
 useSessionStore.setState({ sessions: [{ id: 'main', title: 'Workspace' }, { id: 'worker', parent_session_id: 'main', title: 'BLG-514 · Improve question layout' }] as SessionInfo[] })
 const target = document.createElement('div')
 document.body.appendChild(target)
 const tabs = { tabs: [{ id: 'q', view: { kind: 'question', questionId: 'question', sessionId: 'worker', label: 'Question' } }], visible: ['q'], targetFor: () => target, open: vi.fn(), close: vi.fn(), setQuestionLabel: vi.fn() } as unknown as SessionTabs
 const view = render(<QuestionTabs tabs={tabs} sessionId="worker" />)
 expect(screen.getByText('BLG-514 · Improve question layout')).toBeVisible()
 view.unmount()
 target.remove()
})
