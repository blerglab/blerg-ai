import { describe, expect, it } from 'vitest'
import { drawCategoricalLegend, drawContinuousLegend } from './legend'
import type { ColorScale } from './color'
import type { VisualTheme } from '../theme'

const THEME: VisualTheme = {
  fg: '#f0f3f7', muted: '#bfc7d2', surface: '#171b22', surface2: '#1f242d',
  border: '#313a48', accent: '#ff6a3d', fontFamily: 'sans-serif', fontSize: 14,
  palette: ['#b69cf0'], dark: true,
}

const color: ColorScale = () => THEME.palette[0]

function parse(svg: string) {
  return new DOMParser().parseFromString(`<svg xmlns="http://www.w3.org/2000/svg">${svg}</svg>`, 'image/svg+xml').documentElement
}

describe('drawCategoricalLegend', () => {
  it('marks a series named for a commit with a chip attribute, drawn in the accent token', () => {
    const svg = parse(drawCategoricalLegend(['08025f2c', 'Plain'], color, 0, 0, THEME))
    const chip = svg.querySelector('[data-blerg-chip-key]')!
    const plainText = [...svg.querySelectorAll('text')].find(t => t.textContent === 'Plain')!
    expect(chip.querySelector('text')!.textContent).toBe('08025f2c')
    expect(chip.getAttribute('data-blerg-chip-key')).toBe('commit:08025f2c')
    expect(chip.querySelector('text')!.getAttribute('fill')).toBe(THEME.accent)
    expect(plainText.closest('[data-blerg-chip-key]')).toBeNull()
  })
})


it('fits continuous legend labels to a short heatmap without overlap',()=>{
 const svg=parse(drawContinuousLegend([3,9],()=>THEME.accent,0,0,120,THEME,'merges'))
 const labels=[...svg.querySelectorAll('text')].filter(node=>node.textContent!=='merges')
 const ys=labels.map(node=>Number(node.getAttribute('y'))).sort((a,b)=>a-b)
 for(let i=1;i<ys.length;i++)expect(ys[i]-ys[i-1]).toBeGreaterThanOrEqual(THEME.fontSize*1.2)
})
