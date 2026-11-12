import { expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import RepositoryGraph from './RepositoryGraph'

const { commitDiff } = vi.hoisted(() => ({ commitDiff: vi.fn(async () => ({ text: 'diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n' })) }))
vi.mock('../lib/chatTransport', () => ({ blergTransport: () => ({ files: { commitDiff } }) }))

it('opens a commit diff with the selected repository and runtime generation', async () => {
  const sha = 'a'.repeat(40)
  const member = { member_id: 'library', generation: 7, launch_generation: 'launch' }
  render(<RepositoryGraph name="Library" sessionId="main" memberId="library" depth={50} onMore={() => {}} expanded graph={{ ...member, commits: [{ sha, parents: [], subject: 'Fix library' }], refs: [{ name: 'main', sha }], more: false }} />)
  fireEvent.click(screen.getByRole('button', { name: 'aaaaaaa Fix library' }))
  await waitFor(() => expect(commitDiff).toHaveBeenCalledWith('main', sha, expect.any(AbortSignal), member))
  expect(await screen.findByRole('dialog')).toBeVisible()
})

it('distinguishes loading from unavailable and makes hidden controls visible', () => {
  render(<RepositoryGraph name="app" sessionId="s" depth={50} expanded onMore={() => {}} />)
  expect(screen.getByText('Loading branch history…')).toBeVisible()
})

it('shows hidden formatting characters in branch names and subjects', () => {
  const sha = 'c'.repeat(40)
  render(<RepositoryGraph name="app" sessionId="s" depth={50} expanded onMore={() => {}} graph={{ refs: [{ name: 'topic\u202Ename', sha }], commits: [{ sha, parents: [], subject: 'Fix\u200Bsubject' }], more: false }} />)
  expect(screen.getByText('topic\\u{202E}name')).toBeVisible()
  expect(screen.getByRole('button', { name: 'ccccccc Fix\\u{200B}subject' })).toBeVisible()
})

it('toggles merged chips without hiding commits or showing unknown release rows', () => {
  render(<RepositoryGraph name="app" sessionId="s" depth={50} onMore={() => {}} graph={{
    refs: [{ name: 'main', sha: 'merge' }, { name: 'finished', sha: 'side' }, { name: 'open', sha: 'open' }], more: true,
    commits: [{ sha: 'merge', parents: ['side'], subject: 'Merge' }, { sha: 'side', parents: [], subject: 'Delivered work' }, { sha: 'open', parents: ['side'], subject: 'Open work' }],
    tags: [{ name: 'v1', sha: 'side', contains: ['side'], complete: true }, { name: 'v0', sha: 'absent', contains: [], complete: false }],
  }} />)
  expect(screen.queryByText('finished')).not.toBeInTheDocument()
  expect(screen.getByText('Delivered work', { selector: '.repository-subject-text' })).toBeVisible()
  expect(screen.getByText('open', { selector: '.repository-graph-ref' })).toBeVisible()
  expect(screen.queryByText('v0')).not.toBeInTheDocument()
  expect(screen.getByText('◇ v1')).toBeVisible()
  fireEvent.click(screen.getByRole('checkbox', { name: 'Show merged' }))
  expect(screen.getByText('finished')).toBeVisible()
  fireEvent.click(screen.getByText('v1', { selector: 'strong' }))
  expect(screen.getByRole('region', { name: 'Release v1' })).toHaveTextContent('Delivered work')
})
