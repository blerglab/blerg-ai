// A compositor-owned sample ID: CDP's timestamp can describe delivery of an older
// image. Paint the ID alongside the scene so a delayed JPEG selects its own boxes.
export function chartFrameWitness(enabled = true) {
  const marker = document.createElement('canvas')
  marker.width = 128; marker.height = 8
  marker.style.cssText = 'position:fixed;left:0;top:0;width:128px;height:8px;z-index:2147483647;pointer-events:none'
  marker.setAttribute('aria-hidden', 'true')
  if (enabled) document.body.append(marker)
  let sequence = 0, signature = ''
  return {
    stamp<T extends {frame?: number}>(samples: T[]) {
      const next = JSON.stringify(samples.map(sample => Object.fromEntries(Object.entries(sample).filter(([key]) => key !== 'timestamp' && key !== 'frame'))))
      if (next === signature) { for (const sample of samples) sample.frame = sequence; return }
      signature = next
      const frame = ++sequence
      if (frame > 65535) throw new Error('Chart capture sample limit exceeded')
      const encoded = frame + 0x5aa5 * 65536
      for (const sample of samples) sample.frame = frame
      const context = marker.getContext('2d')!
      for (let bit = 0; bit < 32; bit++) {
        context.fillStyle = ((encoded >>> bit) & 1) ? '#fff' : '#000'
        context.fillRect(bit * 4, 0, 4, 8)
      }
    },
    remove: () => marker.remove(),
  }
}

export interface ChartFrameSample {
  timestamp: number; state: string; stage: string; hosted?: boolean
  surface?: number; rect?: number[]; frame?: number
}

// Self-contained for Playwright's page.evaluate. The marker occupies header pixels,
// outside chart crops, so it cannot supply ink to an otherwise blank surface.
export async function analyzeChartImage({jpeg, samples}: {jpeg: string; samples?: ChartFrameSample[]}) {
  samples ??= (globalThis as unknown as {__blergChartSamples: ChartFrameSample[]}).__blergChartSamples
  const image = new Image(); image.src = 'data:image/jpeg;base64,' + jpeg; await image.decode()
  const canvas = document.createElement('canvas'); canvas.width = 128; canvas.height = 64
  const context = canvas.getContext('2d', {willReadFrequently: true})!
  context.drawImage(image, 0, 0)
  const marker = context.getImageData(0, 0, 128, 8).data
  let frame = 0
  for (let bit = 0; bit < 32; bit++) {
    if (marker[(4 * 128 + bit * 4 + 2) * 4] > 128) frame += 2 ** bit
  }
  if (Math.floor(frame / 65536) !== 0x5aa5) return {frame: 0, analyzed: false, blank: false}
  frame %= 65536
  const batch = samples.filter(sample => sample.frame === frame)
  if (!batch.length) throw new Error('Compositor witness has no measured sample: ' + frame)
  const visibility = batch.find(sample => sample.state === 'view')
  if (!visibility?.hosted || visibility.stage === 'roundtrip') return {frame, analyzed: false, blank: false}
  const seen = new Set<number | undefined>()
  const eligible = batch.filter(sample => { if (seen.has(sample.surface)) return false; if (sample.rect) seen.add(sample.surface); return true }).filter(sample => sample.hosted && sample.rect && ['drag', 'grow', 'narrow', 'settled', 'font', 'theme', 'arrival', 'height', 'short'].includes(sample.stage))
  const blank = eligible.some(sample => {
    const [x, y, width, height] = sample.rect!
    if (width < 4 || height < 4) return false
    context.clearRect(0, 0, 64, 64); context.drawImage(image, x, y, width, height, 0, 0, 64, 64)
    const pixels = context.getImageData(0, 0, 64, 64).data
    const background = [pixels[0], pixels[1], pixels[2]]
    let ink = 0
    for (let p = 0; p < pixels.length; p += 4) if (Math.max(...background.map((c, i) => Math.abs(pixels[p + i] - c))) > 24) ink++
    return ink < 5
  })
  return {frame, analyzed: eligible.length > 0, blank}
}
