import { expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { TopicList } from './HistoryTab'
import { apiFetch } from '../apiFetch'
import { resetRefStoresForTest } from '@blerglab/chat'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
it('renders canonical card references in History findings', async () => {
  resetRefStoresForTest(); resetLiveBoardCardsForTest()
  vi.mocked(apiFetch).mockImplementation(async path => new Response(JSON.stringify(path === '/api/session-boards' ? { boards: [{ id: 'board-a', key: 'BLG', key_aliases: ['OLD'] }] } : { card: { id: 'card', board_id: 'board-a', number: 205, title: 'Work', reference: 'BLG-205' }, url: '' })))
  render(<TopicList sessionId="history-key" now={new Date()} missing={null} onJump={vi.fn()} onOpenArtifact={vi.fn()}
    view={{ status: 'ok', reply: { topics: [], unmarked: null, strokes: [{ id: 's', kind: 'finding', at: new Date().toISOString(), text: 'Fixed OLD-205', state: 'done' }] } } as never} />)
  await waitFor(() => expect(screen.getByRole('button', { name: 'Open card BLG-205: Work' })).toHaveTextContent('BLG-205'))
})
