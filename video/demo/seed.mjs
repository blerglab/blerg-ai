// Seeds the demo install (video/demo/demo.sh runs it): the demo account, a board with cards, and
// sessions on the demo repositories. Everything goes through the APIs the person's browser uses; nothing is written to a database directly.
//
// Writes $BLERG_DEMO_STATE/seed.json: the URLs, the demo login and the ids the capture scripts use.
import fs from 'node:fs'
import path from 'node:path'

const CORE = process.env.BLERG_DEMO_CORE
const BOARD = process.env.BLERG_DEMO_BOARD
const RUNNER = process.env.BLERG_DEMO_RUNNER
const STATE = process.env.BLERG_DEMO_STATE
const USER = process.env.BLERG_DEMO_USER || 'alex'
// A fixed demo password: this install holds nothing and listens on loopback only.
const PASSWORD = 'demo-password-2026'

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
let cookies = ''

async function call(url, { method = 'GET', token, origin, body, redirect = 'follow' } = {}) {
  const headers = { Accept: 'application/json' }
  if (token) headers.Authorization = `Bearer ${token}`
  if (origin) headers.Origin = origin
  if (cookies) headers.Cookie = cookies
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const res = await fetch(url, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), redirect })
  const set = res.headers.getSetCookie?.() || []
  if (set.length) {
    const jar = new Map(cookies.split('; ').filter(Boolean).map((c) => c.split(/=(.*)/s).slice(0, 2)))
    for (const c of set) { const [kv] = c.split(';'); const [k, v] = kv.split(/=(.*)/s); jar.set(k, v) }
    cookies = [...jar].map(([k, v]) => `${k}=${v}`).join('; ')
  }
  return res
}

async function json(url, opts) {
  const res = await call(url, opts)
  const text = await res.text()
  if (!res.ok) throw new Error(`${opts?.method || 'GET'} ${url}: ${res.status} ${text.slice(0, 300)}`)
  return text ? JSON.parse(text) : {}
}

// A person's access token for one component: core's refresh redirect carries it in the fragment.
async function accessToken(origin) {
  const res = await call(`${CORE}/auth/refresh?return_to=${encodeURIComponent(origin + '/')}`, { redirect: 'manual' })
  const loc = res.headers.get('location') || ''
  const m = loc.match(/access_token=([^&]+)/)
  if (!m) throw new Error(`no access token for ${origin} (status ${res.status})`)
  return decodeURIComponent(m[1])
}

// ---------------------------------------------------------------- account
const setup = await call(`${CORE}/auth/setup`, {
  method: 'POST', origin: CORE, body: { code: '', provider_subject: USER, password: PASSWORD },
})
if (!setup.ok) throw new Error(`setup: ${setup.status} ${await setup.text()}`)
const boardToken = await accessToken(BOARD)
console.log(`account ${USER} ready`)

// ---------------------------------------------------------------- board
const board = await json(`${BOARD}/api/boards`, {
  method: 'POST', token: boardToken, origin: BOARD,
  body: {
    name: 'Acme Photos',
    description: 'The photo app: uploads, albums and sharing.',
    columns: ['Backlog', 'Ready', 'In progress', 'Review', 'Done'],
    repos: ['acme-photos', 'acme-shop'],
    run_as_me: false,
  },
})
const full = await json(`${BOARD}/api/boards/${board.id}`, { token: boardToken })
const columns = Object.fromEntries((full.columns || []).map((c) => [c.name, c.id]))
const cards = [
  ['In progress', 'Retry uploads on flaky networks', 'Uploads from the app fail on the first 5xx or dropped connection. Retry with exponential backoff; cover it with tests.', 'high', ['uploads']],
  ['Ready', 'Resume interrupted uploads', 'Large videos restart from zero after a dropped connection. Use the storage service\'s ranged PUT.', 'medium', ['uploads']],
  ['Backlog', 'Shared albums: invite by link', 'An album owner can share a link that lets others add photos.', 'medium', ['albums']],
  ['Backlog', 'Thumbnail cache headers', 'Thumbnails are fetched again on every scroll. Set Cache-Control and an ETag.', 'low', ['performance']],
  ['Review', 'Empty album cover', 'Decide what an empty album shows as its cover.', 'low', ['albums']],
  ['Done', 'Fix the flaky cart total test', 'The cart total test fails about one run in twenty.', 'medium', ['tests'], 'acme-shop'],
]
for (const [col, title, body, priority, tags, repo = 'acme-photos'] of cards) {
  await json(`${BOARD}/api/boards/${board.id}/cards`, {
    method: 'POST', token: boardToken, origin: BOARD,
    body: { title, body, priority, tags, column_id: columns[col], repos: [repo] },
  })
}
console.log(`board ${board.name} with ${cards.length} cards`)

// ---------------------------------------------------------------- sessions
// Started the way the launch sheet starts them (POST /api/sessions as the person), so they are the
// person's own sessions in the sidebar, not automated ones.
const runnerToken = await accessToken(RUNNER)
async function daemonId() {
  for (let i = 0; i < 60; i++) {
    const list = await json(`${RUNNER}/api/daemons`, { token: runnerToken })
    const arr = Array.isArray(list) ? list : (list.daemons || [])
    const d = arr.find((x) => x.status === 'connected' || x.connected) || arr[0]
    if (d) return d.id
    await sleep(1000) // the daemon has not connected yet
  }
  throw new Error('no daemon connected to the demo runner')
}
const daemon = await daemonId()
async function start(body) {
  const out = await json(`${RUNNER}/api/sessions`, {
    method: 'POST', token: runnerToken, origin: RUNNER,
    body: { daemon_id: daemon, kind: 'agent', runtime: 'daemon', interaction: 'interactive', ...body },
  })
  return out.session_id
}
const sessions = {}
sessions.shop = await start({ repo: 'acme-shop', title: 'Flaky cart test', initial_prompt: 'Fix the flaky cart total test.' })
await sleep(1500)
sessions.albums = await start({ repo: 'acme-photos', title: 'Empty album cover', initial_prompt: 'What does the album cover return for an empty album?' })
await sleep(1500)
sessions.uploads = await start({ repo: 'acme-photos', title: 'Retry uploads' })
await sleep(4000)
console.log(`sessions: ${Object.keys(sessions).join(', ')}`)

const seed = { core: CORE, board: BOARD, runner: RUNNER, user: USER, password: PASSWORD, board_id: board.id, sessions }
fs.writeFileSync(path.join(STATE, 'seed.json'), JSON.stringify(seed, null, 2), { mode: 0o600 })
console.log(`wrote ${path.join(STATE, 'seed.json')}`)
