// A short synthesized chime played when a toast appears. Uses the Web Audio API
// so there's no asset to ship or fetch. Best-effort: silently no-ops where audio
// is unavailable (SSR/tests) or blocked by the browser's autoplay policy.

let ctx: AudioContext | null = null

function getCtx(): AudioContext | null {
  if (typeof window === 'undefined') return null
  const AC = window.AudioContext || (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
  if (!AC) return null
  if (!ctx) ctx = new AC()
  return ctx
}

function playNote(c: AudioContext, freq: number, start: number, dur: number): void {
  const osc = c.createOscillator()
  const gain = c.createGain()
  osc.type = 'sine'
  osc.frequency.value = freq
  // Quick attack, exponential decay — a soft bell rather than a flat beep.
  gain.gain.setValueAtTime(0.0001, start)
  gain.gain.exponentialRampToValueAtTime(0.15, start + 0.02)
  gain.gain.exponentialRampToValueAtTime(0.0001, start + dur)
  osc.connect(gain).connect(c.destination)
  osc.start(start)
  osc.stop(start + dur + 0.02)
}

// playToastSound plays a short two-note ascending "ti-doo" chime.
export function playToastSound(): void {
  try {
    const c = getCtx()
    if (!c) return
    if (c.state === 'suspended') void c.resume()
    const now = c.currentTime
    playNote(c, 880, now, 0.12) // A5
    playNote(c, 1174.66, now + 0.1, 0.18) // D6
  } catch {
    // Audio is a nicety; never let it break the toast.
  }
}

// playMessageSound plays a short descending two-note chime — clearly distinct
// from the ascending "ti-doo" of playToastSound. E5 → B4 gives a softer,
// "incoming message" feel.
export function playMessageSound(): void {
  try {
    const c = getCtx()
    if (!c) return
    if (c.state === 'suspended') void c.resume()
    const now = c.currentTime
    playNote(c, 659.25, now, 0.15)       // E5 — first note
    playNote(c, 493.88, now + 0.13, 0.2) // B4 — second note, lower
  } catch {
    // Audio is a nicety; never let it break the toast.
  }
}
