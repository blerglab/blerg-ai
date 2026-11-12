import { it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import InsightsDetails from './InsightsDetails'
import { richInsights } from '../test/insightsFixture'

function renderDetails(props: Parameters<typeof InsightsDetails>[0]) {
 return render(<MemoryRouter><InsightsDetails {...props} /></MemoryRouter>)
}

// BLG-466: within a single workspace's Insights, "sessions" are that workspace's own Main and its
// channels, not separate workspaces. A label must match its own count, for the selected (possibly
// past) range — never a guess from today's live session tree.
it('labels the global report in workspaces, not channels, with the full started count', () => {
 renderDetails({ d: richInsights, section: 'overview' })
 const card = screen.getByTestId('card-started')
 expect(card).toHaveTextContent('Workspaces started')
 expect(card).toHaveTextContent(String(richInsights.sessions.started))
 expect(screen.getByRole('heading', { name: 'Workspaces' })).toBeInTheDocument()
})

it('labels a single-workspace report in channels, with the channel count only (not Main or workers)', () => {
 const d = { ...richInsights, sessions: { ...richInsights.sessions, started: 184, main: 42, channels: 112, workers: 30, alive: 6 } }
 renderDetails({ d, section: 'overview', workspace: 'root' })
 const card = screen.getByTestId('card-started')
 expect(card).toHaveTextContent('Channels started')
 expect(card).toHaveTextContent('112')
 expect(card).not.toHaveTextContent('184')
 expect(card).toHaveTextContent('6 alive now')
 expect(screen.getByRole('heading', { name: 'Channels' })).toBeInTheDocument()
})

it('breaks started down by role with a label matching each count', () => {
 const d = { ...richInsights, sessions: { ...richInsights.sessions, main: 1, channels: 5, workers: 3 } }
 renderDetails({ d, section: 'overview', workspace: 'root' })
 expect(screen.getByText('Started in this range: Main 1 · Channels 5 · Workers 3.')).toBeInTheDocument()
})

it('omits the role breakdown outside a single workspace', () => {
 renderDetails({ d: richInsights, section: 'overview' })
 expect(screen.queryByText(/Started in this range: Main/)).toBeNull()
})

it('names channels alive, not workspaces, in the live section', () => {
 renderDetails({ d: richInsights, section: 'live', workspace: 'root' })
 expect(screen.getByText(/channels alive/)).toBeInTheDocument()
 expect(screen.queryByText(/workspaces alive/)).toBeNull()
})
