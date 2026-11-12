import { describe, expect, it } from 'vitest'
import { drawBandAxisBottom } from './axes'
import type { BandScale } from './scales'
import type { VisualTheme } from '../theme'

const THEME: VisualTheme = {
  fg: '#f0f3f7', muted: '#bfc7d2', surface: '#171b22', surface2: '#1f242d',
  border: '#313a48', accent: '#ff6a3d', fontFamily: 'sans-serif', fontSize: 14,
  palette: ['#b69cf0'], dark: true,
}

function band(domain: readonly string[]): BandScale {
  const bandwidth = 100 / domain.length
  const scale = ((key: string) => { const i = domain.indexOf(key); return i === -1 ? undefined : i * bandwidth }) as BandScale
  scale.domain = domain
  scale.bandwidth = bandwidth
  return scale
}

function parse(svg: string) {
  return new DOMParser().parseFromString(`<svg xmlns="http://www.w3.org/2000/svg">${svg}</svg>`, 'image/svg+xml').documentElement
}

describe('drawBandAxisBottom', () => {
  it('marks a tick whose category is a card key with a chip attribute, drawn in the accent token', () => {
    const svg = parse(drawBandAxisBottom(band(['BLG-452', 'Plain']), {x: 0, y: 0, width: 100, height: 50}, THEME))
    const chip = svg.querySelector('[data-blerg-chip-key]')!
    const plainText = [...svg.querySelectorAll('text')].find(t => t.textContent === 'Plain')!
    expect(chip.querySelector('text')!.textContent).toBe('BLG-452')
    expect(chip.getAttribute('data-blerg-chip-key')).toBe('card:BLG:452')
    expect(chip.querySelector('text')!.getAttribute('fill')).toBe(THEME.accent)
    expect(chip.querySelector('text')!.getAttribute('text-decoration')).toBe('underline')
    expect(plainText.closest('[data-blerg-chip-key]')).toBeNull()
  })

  it('does not mark a category that merely contains a key', () => {
    const svg = parse(drawBandAxisBottom(band(['see BLG-452']), {x: 0, y: 0, width: 100, height: 50}, THEME))
    expect(svg.querySelector('[data-blerg-chip-key]')).toBeNull()
  })
})

it('keeps dense category labels mounted while measuring which ones can fit', () => {
  const labels = Array.from({length: 8}, (_, index) => `Team ${index + 1}`)
  const svg = parse(drawBandAxisBottom(band(labels), {x: 0, y: 0, width: 100, height: 50}, THEME))
  expect([...svg.querySelectorAll('text')].map(text => text.textContent)).toEqual(labels)
  expect([...svg.querySelectorAll('text')].filter(text => text.getAttribute('visibility') !== 'hidden').length).toBeLessThan(8)
})
