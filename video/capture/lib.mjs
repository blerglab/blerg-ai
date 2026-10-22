// Shared helpers for capture scripts: drive the real Blerg UI of the demo install with Playwright
// and record it, writing a take (video + marks) the assembly step (video/build/short.py) cuts from.
//
// A capture script:
//
//   import { take } from '../capture/lib.mjs'
//   await take('diff-review', 'desktop', async (t) => {
//     await t.page.goto(t.runnerUrl(`/sessions/${t.seed.sessions.uploads}`))
//     t.mark('session')                        // a named point, in seconds from the take's start
//     await t.wait('agent works', async () => { // a wait: the assembly speeds it up and says so
//       await t.page.getByText('The diff is above').waitFor()
//     })
//   })
//
// Output: <out>/takes/<short>-<device>.webm (.mp4 for the phone) and <short>-<device>.json:
//   {"video": "...webm", "device": "desktop", "size": [w, h], "marks": {"name": t},
//    "waits": [{"label", "start", "end"}]}
//
// Devices: desktop (1920x1080) and phone (390x693 CSS px, 9:16, recorded at 1080x1920). Usage meters and
// notification prompts are hidden in every take (see NEUTRAL_CSS).
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'

const HERE = path.dirname(fileURLToPath(import.meta.url))
export const REPO = path.resolve(HERE, '..', '..')
const require = createRequire(path.join(REPO, 'package.json'))
const { chromium } = require('playwright')

export const STATE = process.env.BLERG_DEMO_STATE
  || path.join(process.env.XDG_STATE_HOME || path.join(process.env.HOME, '.local', 'state'), 'blerg-demo')
export const OUT = process.env.BLERG_VIDEO_OUT || path.join(REPO, 'video', 'out')

export const DEVICES = {
  desktop: { viewport: { width: 1920, height: 1080 }, deviceScaleFactor: 1, video: { width: 1920, height: 1080 } },
  // 390 px wide, cut to 9:16 so the take is the vertical video's frame as is.
  phone: {
    viewport: { width: 390, height: 693 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true,
    video: { width: 1080, height: 1920 },
  },
}

// What is never on screen: usage meters (real account numbers on a real install; empty here, but
// hidden so a take never depends on that) and notification toasts ("Session idle", messages).
export const NEUTRAL_CSS = `
  .usage-meters, [data-testid="usage-meters"], .usage-limit-resume { display: none !important; }
  [data-testid="toast-session"], [data-testid="toast-message"] { display: none !important; }
`

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

export function loadSeed() {
  const p = path.join(STATE, 'seed.json')
  if (!fs.existsSync(p)) throw new Error(`no ${p}: run video/demo/demo.sh up first`)
  return JSON.parse(fs.readFileSync(p, 'utf8'))
}

async function signIn(browser, seed, device) {
  const ctx = await browser.newContext({ ...DEVICES[device], video: undefined })
  const page = await ctx.newPage()
  await page.goto(`${seed.core}/login`)
  await page.locator('input:not([type=password])').first().fill(seed.user)
  await page.locator('input[type=password]').first().fill(seed.password)
  await page.locator('button[type=submit]').click()
  await page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 30000 })
  // Visit board and runner once so their first load (the refresh round trip) is not filmed.
  for (const origin of [seed.runner, seed.board]) {
    await page.goto(origin + '/')
    await page.waitForLoadState('networkidle').catch(() => {})
  }
  const state = await ctx.storageState()
  await ctx.close()
  return state
}

// take(short, device, fn, opts): one recorded browser session. opts.theme: 'dark' | 'light'.
export async function take(short, device, fn, opts = {}) {
  const seed = loadSeed()
  const dir = path.join(OUT, 'takes')
  fs.mkdirSync(dir, { recursive: true })
  const browser = await chromium.launch()
  const storageState = await signIn(browser, seed, device)
  const dev = DEVICES[device]
  const ctx = await browser.newContext({
    ...dev, storageState, colorScheme: opts.theme || 'dark',
    // Playwright's recorder films CSS pixels, so a 3x phone would come out 390 px wide: those takes
    // are filmed from the browser's own screencast at device pixels instead (screencast() below).
    ...(dev.deviceScaleFactor > 1 ? {} : { recordVideo: { dir, size: dev.video } }),
  })
  await ctx.addInitScript((css) => {
    const add = () => { const s = document.createElement('style'); s.textContent = css; document.documentElement.appendChild(s) }
    if (document.documentElement) add(); else document.addEventListener('DOMContentLoaded', add)
  }, NEUTRAL_CSS)
  const page = await ctx.newPage()
  const t0 = Date.now()
  const cast = dev.deviceScaleFactor > 1 ? await screencast(page, dev, path.join(dir, `.${short}-${device}-frames`), t0) : null
  const now = () => +((Date.now() - t0) / 1000).toFixed(2)
  const marks = {}
  const waits = []
  const t = {
    seed, page, ctx, device, now,
    runnerUrl: (p = '/') => seed.runner + p,
    boardUrl: (p = '/') => seed.board + p,
    coreUrl: (p = '/') => seed.core + p,
    mark(name) { marks[name] = now(); console.log(`  mark ${name} ${marks[name]}`) },
    // A wait that is not interesting to watch in real time. The assembly plays it faster and
    // shows "sped up xN" while it does: the footage stays honest about time.
    async wait(label, f) {
      const start = now()
      const r = await f()
      waits.push({ label, start, end: now() })
      console.log(`  wait ${label} ${start}-${now()}`)
      return r
    },
    // Human-paced input, so typing reads on screen.
    async type(locator, text, delay = 38) { await locator.click(); await locator.pressSequentially(text, { delay }) },
    pause: (s = 0.8) => sleep(s * 1000),
    async shot(name) { await page.screenshot({ path: path.join(dir, `${short}-${device}-${name}.png`) }) },
  }
  let failed = null
  try {
    await fn(t)
  } catch (e) {
    failed = e
    await page.screenshot({ path: path.join(dir, `${short}-${device}-error.png`) }).catch(() => {})
  }
  await sleep(600)
  const duration = now()
  const video = cast ? null : page.video()
  const castFrames = cast ? await cast.stop() : null
  await ctx.close()
  await browser.close()
  let dest
  if (cast) {
    dest = path.join(dir, `${short}-${device}.mp4`)
    encodeFrames(castFrames, duration, dev.video, dest)
    fs.rmSync(cast.dir, { recursive: true, force: true })
  } else {
    dest = path.join(dir, `${short}-${device}.webm`)
    fs.renameSync(await video.path(), dest)
  }
  const meta = { video: path.basename(dest), device, size: [dev.video.width, dev.video.height], duration, marks, waits }
  fs.writeFileSync(path.join(dir, `${short}-${device}.json`), JSON.stringify(meta, null, 2))
  console.log(`wrote ${dest}`)
  if (failed) throw failed
  return meta
}

// The browser's screencast at device pixels: every frame it paints, with the time it arrived.
async function screencast(page, dev, framesDir, t0) {
  fs.rmSync(framesDir, { recursive: true, force: true })
  fs.mkdirSync(framesDir, { recursive: true })
  const cdp = await page.context().newCDPSession(page)
  const frames = []
  cdp.on('Page.screencastFrame', (f) => {
    const file = path.join(framesDir, `f${String(frames.length).padStart(6, '0')}.jpg`)
    fs.writeFileSync(file, Buffer.from(f.data, 'base64'))
    frames.push({ file, t: (Date.now() - t0) / 1000 })
    cdp.send('Page.screencastFrameAck', { sessionId: f.sessionId }).catch(() => {})
  })
  await cdp.send('Page.startScreencast', {
    format: 'jpeg', quality: 92, maxWidth: dev.video.width, maxHeight: dev.video.height, everyNthFrame: 1,
  })
  return {
    dir: framesDir,
    async stop() { await cdp.send('Page.stopScreencast').catch(() => {}); return frames },
  }
}

// Frames with arrival times -> a constant-rate video of the take's length (each frame shown until
// the next one arrived; the first one from 0 s), at exactly the device's video size.
function encodeFrames(frames, duration, size, dest) {
  if (!frames.length) throw new Error('the screencast sent no frames')
  const lines = []
  frames.forEach((f, i) => {
    const start = i === 0 ? 0 : f.t
    const end = i + 1 < frames.length ? frames[i + 1].t : duration
    lines.push(`file '${f.file}'`, `duration ${Math.max(0.001, end - start).toFixed(3)}`)
  })
  lines.push(`file '${frames[frames.length - 1].file}'`)
  const list = path.join(path.dirname(frames[0].file), 'list.txt')
  fs.writeFileSync(list, lines.join('\n') + '\n')
  const { width: w, height: h } = size
  execFileSync('ffmpeg', ['-v', 'error', '-y', '-f', 'concat', '-safe', '0', '-i', list,
    '-vf', `scale=${w}:${h}:force_original_aspect_ratio=decrease,pad=${w}:${h}:(ow-iw)/2:(oh-ih)/2:color=0x0d1117,fps=30,format=yuv420p`,
    '-c:v', 'libx264', '-preset', 'veryfast', '-crf', '16', dest])
}
