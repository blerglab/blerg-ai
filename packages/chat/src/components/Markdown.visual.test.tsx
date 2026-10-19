import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import Markdown from './Markdown'
import { TextView } from './ArtifactViewer'
import type { ArtifactInfo } from '../transport/types'

vi.mock('./VisualBlock', () => ({ default: ({ kind, source }: { kind: string; source: string }) => <div data-testid="visual" data-kind={kind}>{source}</div> }))
const fence = (lang: string, source: string, close = true) => '```' + lang + '\n' + source + '\n' + (close ? '```' : '')

describe('shared visual output routing', () => {
  it.each(['mermaid', 'vega-lite'])('routes a closed %s fence to the shared renderer', kind => {
    render(<Markdown text={fence(kind, 'source')} />)
    expect(screen.getByTestId('visual')).toHaveAttribute('data-kind', kind)
    expect(screen.getByTestId('visual')).toHaveTextContent('source')
  })
  it('keeps incomplete streamed diagrams as code', () => {
    render(<Markdown text={fence('mermaid', 'flowchart LR\nA --> B', false)} />)
    expect(screen.queryByTestId('visual')).toBeNull()
    expect(screen.getByText(/flowchart/).closest('pre')).not.toBeNull()
  })
  it.each([
    ['diagram.mmd', 'text', 'flowchart LR\nA --> B', 'mermaid'],
    ['chart.vl.json', 'json', '{"mark":"bar"}', 'vega-lite'],
    ['chart.json', 'json', '{"$schema":"https://vega.github.io/schema/vega-lite/v6.json","mark":"point"}', 'vega-lite'],
    ['chart.json', 'json', '{"$schema":"https://vega.github.io/schema/vega-lite/v6.5.0.json","mark":"point"}', 'vega-lite'],
  ])('uses the renderer for the file %s', (name, view, text, kind) => {
    render(<TextView artifact={{ name, view } as ArtifactInfo} text={text} showAll onShowAll={() => {}} />)
    expect(screen.getByTestId('visual')).toHaveAttribute('data-kind', kind)
  })
  it('does not treat ordinary JSON data as a chart', () => {
    render(<TextView artifact={{ name: 'data.json', view: 'json' } as ArtifactInfo} text={'{"mark":"bar"}'} showAll onShowAll={() => {}} />)
    expect(screen.queryByTestId('visual')).toBeNull()
  })
})
