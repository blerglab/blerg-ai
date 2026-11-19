import { expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import LayoutPreview from './LayoutPreview'
import { layoutSummary } from '../lib/layoutPreview'
import type { WorkspaceLayout } from '../lib/workspaceLayouts'
const layout: WorkspaceLayout = { tabs: [{ id: 'insights', view: { kind: 'insights' }, preview: false }], active: 'insights', focused: 'right', viewport: { width: 1000, height: 800 }, panels: { explorer: { open: true, width: 200 }, 'session-panel': { open: true, width: 200 } }, layout: { type: 'split', id: 'split', axis: 'vertical', ratio: .25, first: { type: 'group', id: 'left', tabs: ['chat'], active: 'chat' }, second: { type: 'group', id: 'right', tabs: ['insights'], active: 'insights' } } }
it('draws saved split proportions with explorer and panel boxes', () => {
  const { container } = render(<LayoutPreview layout={layout} name="Research" />)
  expect(screen.getByRole('img', { name: 'Pane preview for Research' })).toBeTruthy()
  const boxes = container.querySelectorAll('[data-pane="group"] > rect')
  expect(boxes).toHaveLength(2)
  expect(Number(boxes[0].getAttribute('width'))).toBeLessThan(Number(boxes[1].getAttribute('width')) / 2)
  expect(container.querySelector('[data-pane="explorer"]')).not.toBeNull()
  expect(container.querySelector('[data-pane="panel"]')).not.toBeNull()
  expect(layoutSummary(layout)).toBe('Main · Insights · explorer open · panel open')
})
it('does not invent Main in the summary of a saved empty workspace', () => {
  expect(layoutSummary({ tabs: [], mainClosed: true, active: '', focused: 'main', panels: {}, layout: { type: 'group', id: 'main', tabs: [], active: '' } })).toBe('No tabs open')
})
