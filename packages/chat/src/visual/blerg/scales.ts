// BLG-452 design doc §3.4: domain->range mapping, reimplemented directly rather than through
// vega-scale, but matching Vega's own math exactly where it matters for the differential gate
// (§3.10) — nice()'d linear domains and band-scale geometry are ported from d3-scale's own
// algorithm (both already transitive dependencies, §3.8/H6), not reinvented.
import { tickIncrement } from 'd3-array'

export interface LinearScale {
  domain: [number, number]
  (value: number): number
}

/** Matches d3-scale's linearish().nice() exactly (same tickIncrement loop), so a niced domain is
 *  byte-identical to Vega's. count defaults to 10, Vega's own axis/scale default tick count. */
export function niceLinearDomain(domain: [number, number], count = 10): [number, number] {
  let [start, stop] = domain
  const reversed = stop < start
  if (reversed) [start, stop] = [stop, start]
  let prestep: number | undefined
  for (let i = 0; i < 10; i++) {
    const step = tickIncrement(start, stop, count)
    if (step === prestep) break
    if (step > 0) { start = Math.floor(start / step) * step; stop = Math.ceil(stop / step) * step }
    else if (step < 0) { start = Math.ceil(start * step) / step; stop = Math.floor(stop * step) / step }
    else break
    prestep = step
  }
  return reversed ? [stop, start] : [start, stop]
}

export function linearScale(domain: [number, number], range: [number, number]): LinearScale {
  const [d0, d1] = domain, [r0, r1] = range
  const scale = ((value: number) => d1 === d0 ? r0 : r0 + ((value - d0) / (d1 - d0)) * (r1 - r0)) as LinearScale
  scale.domain = domain
  return scale
}

export interface BandScale {
  (key: string): number | undefined
  bandwidth: number
  domain: readonly string[]
}

/** Ports d3-scale's band() rescale() formula exactly (step/start/bandwidth), so band positions
 *  match Vega's band scale pixel-for-pixel. align defaults to 0.5, d3's own default. */
export function bandScale(domain: readonly string[], range: [number, number], paddingInner: number, paddingOuter: number, align = 0.5): BandScale {
  const n = domain.length
  const [r0, r1] = range
  const reverse = r1 < r0
  const start0 = reverse ? r1 : r0
  const stop0 = reverse ? r0 : r1
  const step = (stop0 - start0) / Math.max(1, n - paddingInner + paddingOuter * 2)
  const start = start0 + (stop0 - start0 - step * (n - paddingInner)) * align
  const bandwidth = step * (1 - paddingInner)
  const positions = new Map(domain.map((key, i) => [key, reverse ? start + step * (n - 1 - i) : start + step * i]))
  const scale = ((key: string) => positions.get(key)) as BandScale
  scale.bandwidth = bandwidth
  scale.domain = domain
  return scale
}
