import { describe, expect, it } from 'vitest'
import { modelDisplayName, modelDisplayNames } from './modelDisplay'
const lightning = 'nvidia-nemotron-3.5-lightning-30b-a3b-nvfp4'
const superVL = 'custom:local-vision/NVIDIA-Nemotron-3.5-Super-VL-120B-A12B-NVFP4'
describe('model display names', () => {
  it('removes route, vendor, version, size and quantization from concise names', () => {
    expect(modelDisplayName(lightning)).toBe('Nemotron Lightning')
    expect(modelDisplayName(superVL)).toBe('Nemotron Super VL')
    expect(modelDisplayName('nvidia/NVIDIA-Nemotron-3.5-Super-VL-120B-A12B-NVFP4')).toBe('Nemotron Super VL')
  })
  it('preserves unknown names and supplied human labels', () => {
    expect(modelDisplayName('custom:lab/unknown-4b-q4')).toBe('custom:lab/unknown-4b-q4')
    expect(modelDisplayName('claude-opus-5-5', 'Opus 5.5')).toBe('Opus 5.5')
    expect(modelDisplayName('claude-sonnet-5')).toBe('sonnet-5')
    expect(modelDisplayName('not-nemotron-lightning')).toBe('not-nemotron-lightning')
  })
  it('adds size/quantization only for colliding labels', () => {
    const other = lightning.replace('30b-a3b-nvfp4', '49b-q8')
    const names = modelDisplayNames([{ id: lightning }, { id: other }, { id: superVL }])
    expect(names.get(lightning)).toContain('30B')
    expect(names.get(other)).toContain('49B')
    expect(names.get(superVL)).toBe('Nemotron Super VL')
  })
  it('keeps distinct versions distinguishable when their family and size match', () => {
    const other = lightning.replace('3.5', '4.0')
    const names = modelDisplayNames([{ id: lightning }, { id: other }])
    expect(names.get(lightning)).toContain('v3.5')
    expect(names.get(other)).toContain('v4.0')
  })
  it('falls back to exact IDs for unrecognized variant collisions', () => {
    const a = `${lightning}-base`, b = `${lightning}-instruct`
    const names = modelDisplayNames([{ id: a }, { id: b }])
    expect(names.get(a)).not.toBe(names.get(b))
    expect(names.get(a)).toContain(a)
    expect(names.get(b)).toContain(b)
  })
  it('never collides with a supplied name matching a generated fallback', () => {
    const a = `${lightning}-base`, b = `${lightning}-instruct`
    const known = modelDisplayNames([{ id: a }, { id: b }])
    const names = modelDisplayNames([{ id: a }, { id: b }, { id: 'unknown-model', name: known.get(a) }])
    expect(new Set(names.values()).size).toBe(3)
  })
  it('distinguishes routes without changing model IDs', () => {
    const a = `custom:alpha/${lightning}`, b = `custom:beta/${lightning}`
    const models = [{ id: a }, { id: b }], before = JSON.stringify(models)
    const names = modelDisplayNames(models)
    expect(names.get(a)).not.toBe(names.get(b))
    expect(names.get(a)).toContain('alpha')
    expect(names.get(b)).toContain('beta')
    expect(JSON.stringify(models)).toBe(before)
  })
})
