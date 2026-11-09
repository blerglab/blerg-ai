// Wall-clock browser samples include time when the shared machine deschedules the
// browser. Use the typical sample; retain maxima in diagnostics and render counts
// as deterministic checks that typing does not wake unrelated UI.
export function medianSample(samples: readonly number[]): number {
 if (!samples.length) throw new Error('Performance measurement needs samples')
 const sorted = [...samples].sort((a, b) => a - b)
 const middle = Math.floor(sorted.length / 2)
 return sorted.length % 2 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2
}
