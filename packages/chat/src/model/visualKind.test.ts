import { describe, expect, it } from 'vitest'
import { visualFence, visualFile } from './visualKind'

describe('visual output identification', () => {
  it('matches visual fences without importing renderers', () => {
    expect(visualFence('language-mermaid')).toBe('mermaid')
    expect(visualFence('language-vega-lite title=chart')).toBe('vega-lite')
    expect(visualFence('json')).toBeNull()
    expect(visualFence('card:mermaid')).toBeNull()
  })
  it('only recognizes supported source files in textual views', () => {
    expect(visualFile('test.MMD', 'text', 'source')).toBe('mermaid')
    expect(visualFile('test.mmd', 'image', 'source')).toBeNull()
    expect(visualFile('chart.vega-lite.json', 'json', 'invalid')).toBe('vega-lite')
    expect(visualFile('chart.json', 'json', '{')).toBeNull()
    expect(visualFile('chart.json', 'json', '{"$schema":"https://vega.github.io/schema/vega-lite/v6.5.0.json"}')).toBe('vega-lite')
    expect(visualFile('chart.json', 'json', '{"$schema":"http://vega.github.io/schema/vega-lite/v6.json"}')).toBeNull()
    expect(visualFile('chart.json', 'json', '{"$schema":"https://example.com/vega-lite/v6.json"}')).toBeNull()
    expect(visualFile('chart.json', 'json', '{"$schema":"https://vega.github.io/schema/vega/v6.json"}')).toBeNull()
  })
})
