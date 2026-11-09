#!/usr/bin/env node
// BLG-453: drives the real, built runner and board frontends (served by the
// real runner and board servers, against the real core identity service)
// with Playwright, timing each named scenario (eight, including cold-load). Invoked by
// `release-perf` (runner/cmd/release-perf); never run as part of the
// ordinary gate. See docs/design/release-performance.md.
//
// Config comes entirely from environment variables (no CLI flags) so the Go
// orchestrator can set them exactly, including ephemeral ports it just
// allocated:
//   RELEASE_PERF_CORE_URL, RELEASE_PERF_RUNNER_URL, RELEASE_PERF_BOARD_URL
//   RELEASE_PERF_ACCOUNT_USERNAME, RELEASE_PERF_ACCOUNT_PASSWORD
//   RELEASE_PERF_REPS (repetitions per scenario, excluding the warm-up)
//   RELEASE_PERF_OUT (path to write the JSON result to)
import { chromium } from 'playwright'
import { writeFile } from 'node:fs/promises'
import { redactUrl } from './redact.mjs'
import { pickServerTiming } from './serverTiming.mjs'
import { runAcceptance, closeBlackboardTab, runBootTrials, captureTypingKeydown, typingMeasurement } from './release-perf-acceptance.mjs'

const env = (name, fallback) => process.env[name] ?? fallback
const CORE_URL = env('RELEASE_PERF_CORE_URL')
const RUNNER_URL = env('RELEASE_PERF_RUNNER_URL')
const BOARD_URL = env('RELEASE_PERF_BOARD_URL')
const USERNAME = env('RELEASE_PERF_ACCOUNT_USERNAME', 'release-perf')
const PASSWORD = env('RELEASE_PERF_ACCOUNT_PASSWORD')
const REPS = Number(env('RELEASE_PERF_REPS', '11'))
const OUT = env('RELEASE_PERF_OUT')
const BOARD_ID = env('RELEASE_PERF_BOARD_ID')
const LONG_SESSION_ID = env('RELEASE_PERF_LONG_SESSION_ID')
const SWITCH_TARGET_ID = env('RELEASE_PERF_SWITCH_TARGET_ID')

// Browser console errors / uncaught exceptions since the last clear(),
// so a mark timeout's debug payload can show the real cause instead of
// just "nothing happened" — a crashed render looks identical to a slow
// one from the DOM alone.
const pageErrors = []
// Frame navigations since the last clear(), so a mark timeout's debug can
// show whether the auth-refresh bounce (a real browser navigation to core
// and back) ever actually happened, or never fired/never returned.
const navigations = []
// WebSocket lifecycle (open/close, with close code) since the last
// clear(), to check a theory straight from the resource-timing gap: a
// second-long silence before a lone API call fires, matching ws.ts's own
// reconnect backoff (starts at 2000ms, doubles on each failure) almost
// exactly — so a cold page's WS connection likely opens before the
// access token is ready, gets closed by the server, and only reconnects
// on its own blind backoff timer rather than retrying as soon as the
// token the page already obtained (via the auth bounce) is available.
const wsEvents = []
const documents = []

if (!CORE_URL || !RUNNER_URL || !BOARD_URL || !PASSWORD || !OUT) {
  console.error('RELEASE_PERF_CORE_URL, RELEASE_PERF_RUNNER_URL, RELEASE_PERF_BOARD_URL, RELEASE_PERF_ACCOUNT_PASSWORD and RELEASE_PERF_OUT are required')
  process.exit(2)
}

// Reads every performance.mark entry with this name recorded after `since`
// (a Date.now() wall-clock epoch ms from just before the action started).
// A scenario action is often a full navigation (page.goto) — Playwright
// keeps driving the same Page object across it, but the document's own
// performance.now()/timeOrigin resets to the new document's navigation
// start, so comparing a stale pre-navigation performance.now() value
// against marks recorded on the new document would never match (every
// mark's startTime is small and positive, "since" from the old, longer-
// lived document is typically much larger) — hence converting every
// mark's relative startTime to the same wall-clock epoch via timeOrigin,
// which is stable across the comparison regardless of which document
// recorded it.
// ensureWorkspace1Visible expands the sidebar's collapsed "History"
// disclosure if needed. Every seeded workspace but the long-transcript one
// is 'stopped' (aeeb58e9 only needed one live one, for typing), so
// SessionList.tsx's HistorySection groups the rest behind "History",
// collapsed by default — a real click on workspace 1's title times out
// against a DOM node that doesn't exist yet otherwise. That expanded state
// is local, unpersisted React state: any full page.goto (cold-load's own
// repetitions, or board-tab's reload in between workspace-switch and
// transcript-open) resets it, so this checks and re-expands on demand
// rather than assuming one expand lasts the whole run.
async function ensureWorkspace1Visible(page) {
  if (await page.locator('[title="Release-perf workspace 1"]').count() === 0) {
    await page.getByRole('button', { name: /History/ }).click().catch(() => {})
  }
}

async function markedAfter(page, name, since) {
  return page.evaluate(([n, s]) => {
    const entries = performance.getEntriesByName(n, 'mark')
    const hit = entries.find(e => (performance.timeOrigin + e.startTime) >= s)
    return hit ? performance.timeOrigin + hit.startTime : null
  }, [name, since])
}

async function waitForMark(page, name, since, timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const t = await markedAfter(page, name, since)
    if (t !== null) return t
    if (Date.now() > deadline) {
      const debug = await page.evaluate(() => ({
        url: location.href,
        buttons: [...document.querySelectorAll('button')].map(b => b.getAttribute('aria-label') || b.textContent?.trim()).filter(Boolean),
        marks: performance.getEntriesByType('mark').map(m => m.name),
        bodyText: document.body.textContent?.slice(0, 1500),
        // Which tab is active/visible: a mark that depends on a
        // particular tab's content being shown (transcript-rendered)
        // times out silently if some earlier scenario left a different
        // tab active instead — this is usually why.
        tabs: [...document.querySelectorAll('[role="tab"]')].map(t => ({ name: t.getAttribute('aria-label') || t.textContent?.trim(), selected: t.getAttribute('aria-selected') })),
      })).catch(e => ({ evalError: String(e) }))
      if (debug.url) debug.url = redactUrl(debug.url)
      throw new Error(`mark ${name} never appeared within ${timeoutMs}ms; debug: ${JSON.stringify(debug)}; pageErrors: ${JSON.stringify(pageErrors)}; navigations: ${JSON.stringify(navigations)}`)
    }
    await new Promise(r => setTimeout(r, 25))
  }
}

// One Server-Timing-bearing response, captured by name substring, since the
// last clear(). Returns its total;dur=N.NNN value in ms, or null.
function captureServerTiming(page, store) {
  page.on('response', resp => {
    const header = resp.headers()['server-timing']
    if (!header) return
    const m = /total;dur=([0-9.]+)/.exec(header)
    if (!m) return
    store.push({ url: resp.url(), ms: Number(m[1]) })
  })
}

// DIAGNOSE names scenarios to break down with Navigation/Resource Timing
// plus our own marks, for the first counted repetition only — set by
// RELEASE_PERF_DIAGNOSE=name1,name2,... Investigates "why is the
// client-observed time so much larger than the server-reported one."
const DIAGNOSE = new Set(env('RELEASE_PERF_DIAGNOSE', '').split(',').filter(Boolean))

// breakdownTiming reads this document's own Navigation Timing entry, the
// script/fetch/xhr Resource Timing entries, and every performance.mark —
// everything needed to tell apart navigation/TTFB, script download+eval,
// an auth bounce round trip, the first API fetch, and the readiness mark
// itself, without guessing from wall-clock deltas alone.
async function breakdownTiming(page) {
  return page.evaluate(() => {
    const nav = performance.getEntriesByType('navigation')[0]
    const resources = performance.getEntriesByType('resource')
      .filter(r => ['script', 'fetch', 'xmlhttprequest'].includes(r.initiatorType))
      .map(r => ({ name: new URL(r.name).pathname, type: r.initiatorType, startTime: r.startTime, responseEnd: r.responseEnd, transferSize: r.transferSize }))
    return {
      timeOrigin: performance.timeOrigin,
      nav: nav ? {
        redirectCount: nav.redirectCount,
        fetchStart: nav.fetchStart,
        domainLookupStart: nav.domainLookupStart,
        connectStart: nav.connectStart,
        requestStart: nav.requestStart,
        responseStart: nav.responseStart, // TTFB
        responseEnd: nav.responseEnd,
        domInteractive: nav.domInteractive,
        domContentLoadedEventStart: nav.domContentLoadedEventStart,
        domComplete: nav.domComplete,
        loadEventEnd: nav.loadEventEnd,
      } : null,
      resources,
      marks: performance.getEntriesByType('mark').map(m => ({ name: m.name, startTime: m.startTime })),
    }
  })
}

async function measure(page, { name, action, pre, markName, serverTimingLog, serverEndpoint, timeoutMs }, diagnose) {
  // pre runs before the clock starts: setup a scenario needs between
  // repetitions (e.g. leaving the destination so the next repetition has
  // somewhere real to switch back from) that isn't part of what's named.
  if (pre) await pre()
  serverTimingLog.length = 0
  pageErrors.length = 0
  navigations.length = 0
  wsEvents.length = 0
  documents.length = 0
  const stopTyping = name === 'typing' ? await captureTypingKeydown(page) : null
  const since = await page.evaluate(() => Date.now())
  try {
    await action()
  } catch (err) {
    if (stopTyping) await stopTyping()
    const debug = await page.evaluate(() => ({
      url: location.href,
      buttons: [...document.querySelectorAll('button')].map(b => b.getAttribute('aria-label') || b.textContent?.trim()).filter(Boolean),
      marks: performance.getEntriesByType('mark').map(m => m.name),
    })).catch(e => ({ evalError: String(e) }))
    if (debug.url) debug.url = redactUrl(debug.url)
    throw new Error(`${name}'s action failed: ${err.message}; debug: ${JSON.stringify(debug)}`)
  }
  const keydownAt = stopTyping ? await stopTyping() : since
  if (!Number.isFinite(keydownAt)) throw new Error('typing_keydown_missing')
  const markAt = await waitForMark(page, markName, keydownAt, timeoutMs)
  const measured = stopTyping ? typingMeasurement(since, keydownAt, markAt) : { clientMs: markAt - since }
  const { clientMs } = measured
  if (diagnose) {
    const b = await breakdownTiming(page)
    const o = b.timeOrigin
    const rel = (t) => t === null || t === undefined ? null : Math.round((o + t) - since)
    console.error(`DIAGNOSE ${name}: client_ms=${Math.round(clientMs)} navigations=${navigations.length} navigations_detail=${JSON.stringify(navigations.map(n => ({ url: n.url, at_ms: n.at - since })))}`)
    if (b.nav) {
      console.error(`DIAGNOSE ${name}: nav_ms_since_action=${JSON.stringify({
        fetchStart: rel(b.nav.fetchStart), ttfb_responseStart: rel(b.nav.responseStart), responseEnd: rel(b.nav.responseEnd),
        domInteractive: rel(b.nav.domInteractive), domContentLoaded: rel(b.nav.domContentLoadedEventStart),
        domComplete: rel(b.nav.domComplete), loadEventEnd: rel(b.nav.loadEventEnd), redirectCount: b.nav.redirectCount,
      })}`)
    }
    console.error(`DIAGNOSE ${name}: resources=${JSON.stringify(b.resources.map(r => ({ name: redactUrl(r.name).replace(/^https?:\/\/[^/]+/, ''), type: r.type, start_ms: rel(r.startTime), end_ms: rel(r.responseEnd), bytes: r.transferSize })))}`)
    console.error(`DIAGNOSE ${name}: marks=${JSON.stringify(b.marks.map(m => ({ name: m.name, at_ms: rel(m.startTime) })))}`)
    console.error(`DIAGNOSE ${name}: websockets=${JSON.stringify(wsEvents.filter(e => e.at >= since && !['send', 'recv'].includes(e.type)).map(e => ({ type: e.type, at_ms: e.at - since, err: e.err })))}`)
  }
  // The Server-Timing header any Server-Timing-bearing response in the
  // window carries, taking the plain "last one" regardless of its URL
  // (Codex review of d7e50390, MED d) silently reported an unrelated
  // background request's timing for a scenario that made several
  // requests, or one that made none of its own at all (search is
  // client-side only; cold-load and workspace-switch/transcript-open's
  // real data arrives over the WebSocket, not a REST response this
  // mechanism can see) — serverEndpoint names the one request each
  // scenario that actually has a matchable REST endpoint expects; its
  // absence means "no server number applies here," not "guess."
  const serverMs = pickServerTiming(serverTimingLog, serverEndpoint)
  const coldLoad = name === 'cold-load' ? {
    clientMs,
    documents: documents.map(d => ({ ...d, atMs: d.at - since, at: undefined })),
    timing: await breakdownTiming(page),
  } : undefined
  return { ...measured, serverMs, coldLoad }
}

async function repeated(page, scenario, n) {
  const client = [], server = [], coldLoads = [], preKeydown = [], commandToCommit = []
  // One discarded warm-up iteration, per docs/design/release-performance.md.
  await measure(page, scenario)
  for (let i = 0; i < n; i++) {
    const { clientMs, serverMs, coldLoad, preKeydownMs, commandToCommitMs } = await measure(page, scenario, i === 0 && DIAGNOSE.has(scenario.name))
    client.push(clientMs)
    if (coldLoad) coldLoads.push(coldLoad)
    if (serverMs !== null) server.push(serverMs)
    if (preKeydownMs !== undefined) { preKeydown.push(preKeydownMs); commandToCommit.push(commandToCommitMs) }
  }
  return { name: scenario.name, clientSamplesMs: client, serverSamplesMs: server, ...(coldLoads.length ? { coldLoads } : {}), ...(preKeydown.length ? { preKeydownSamplesMs: preKeydown, commandToCommitSamplesMs: commandToCommit } : {}) }
}

// Use the same invalidation as a real identity change to discard retained views.
// Reconnection is setup, outside the measured blackboard/transcript action.
async function resetRetainedViews(page) {
  const since = await page.evaluate(() => {
    const since = Date.now()
    window.dispatchEvent(new Event('blerg-identity-changed'))
    return since
  })
  await waitForMark(page, 'diagnose-initial_state', since)
}

async function main() {
  const browser = await chromium.launch()
  const context = await browser.newContext()
  // Asks each server's ServerTimingMiddleware (runner/internal/server/
  // releaseperftiming.go) for a Server-Timing response header — it also
  // requires BLERG_RELEASE_PERF_SERVER_TIMING=1 in the server's own
  // environment (set by cmd/release-perf), so this header alone does
  // nothing against a normal install. Scoped to fetch/xhr requests only
  // (not websocket, not the document navigation itself): a
  // context-wide setExtraHTTPHeaders put this on the /ws/browser upgrade
  // too, and the middleware's ResponseWriter wrapper doesn't implement
  // http.Hijacker, which the upgrade needs — that broke every
  // session-scoped scenario's WebSocket connection (500 on handshake).
  await context.route('**/*', (route) => {
    const type = route.request().resourceType()
    if (type === 'fetch' || type === 'xhr') {
      return route.continue({ headers: { ...route.request().headers(), 'X-Blerg-Release-Perf': '1' } })
    }
    return route.continue()
  })
  await context.addInitScript(() => {
    window.__BLERG_RELEASE_PERF__ = true
    const NativeSocket = window.WebSocket
    window.WebSocket = class extends NativeSocket {
      constructor(...args) {
        performance.mark('diagnose-ws-construct')
        super(...args)
        this.addEventListener('open', () => performance.mark('diagnose-ws-open'))
        this.addEventListener('message', event => {
          let type
          try { type = JSON.parse(event.data).type } catch { return }
          if (['initial_state', 'subscribed', 'agent_events', 'agent_events_replay_done'].includes(type)) performance.mark('diagnose-' + type)
        })
      }
    }
    new PerformanceObserver(list => {
      for (const entry of list.getEntries()) performance.mark('diagnose-' + entry.name, {startTime: entry.startTime})
    }).observe({type: 'paint', buffered: true})
  })
  const page = await context.newPage()
  const serverTimingLog = []
  captureServerTiming(page, serverTimingLog)
  page.on('response', response => {
    const request = response.request()
    if (request.resourceType() !== 'document') return
    const timing = request.timing()
    documents.push({
      path: new URL(request.url()).pathname,
      topLevel: request.frame() === page.mainFrame(),
      status: response.status(), at: Date.now(),
      requestMs: timing.requestStart,
      responseMs: timing.responseStart,
    })
  })
  page.on('console', msg => { if (msg.type() === 'error') pageErrors.push(msg.text()) })
  page.on('pageerror', err => pageErrors.push(String(err)))
  page.on('framenavigated', frame => { if (frame === page.mainFrame()) navigations.push({ url: redactUrl(frame.url()), at: Date.now() }) })
  page.on('websocket', ws => {
    const url = redactUrl(ws.url())
    wsEvents.push({ type: 'open', url, at: Date.now() })
    ws.on('close', () => wsEvents.push({ type: 'close', url, at: Date.now() }))
    ws.on('socketerror', (err) => wsEvents.push({ type: 'error', url, at: Date.now(), err: String(err) }))
    ws.on('framesent', (f) => wsEvents.push({ type: 'send', url, at: Date.now(), payload: String(f.payload).slice(0, 200) }))
    ws.on('framereceived', (f) => wsEvents.push({ type: 'recv', url, at: Date.now(), payload: String(f.payload).slice(0, 200) }))
  })

  // Sign in once on core; the browser keeps its session cookie for the rest
  // of the run. Runner/board redirect to core's own origin to mint a fresh
  // access token (authClient.ts's redirectToRefresh) and back — Playwright
  // never handles a token directly, exactly like a real user.
  await page.goto(CORE_URL + '/setup')
  const onSetupForm = await page.getByLabel('Username').count() > 0 && page.url().includes('/setup')
  if (onSetupForm) {
    await page.getByLabel('Username').fill(USERNAME)
    await page.getByLabel('Password', { exact: true }).fill(PASSWORD)
    await page.getByLabel('Confirm password').fill(PASSWORD)
    await Promise.all([page.waitForLoadState('networkidle').catch(() => {}), page.getByRole('button', { name: /create account/i }).click()])
  }
  // Setup already complete (a prior run's account exists), or the create
  // failed because it is: sign in instead.
  if (!onSetupForm || page.url().includes('/setup') || page.url().includes('/login')) {
    await page.goto(CORE_URL + '/login')
    await page.getByLabel('Username').fill(USERNAME)
    await page.getByLabel('Password', { exact: true }).fill(PASSWORD)
    await Promise.all([page.waitForLoadState('networkidle').catch(() => {}), page.getByRole('button', { name: /sign in|log in/i }).click()])
  }

  if (env('RELEASE_PERF_MODE') === 'account-only') {
    // Only confirm the real sign-up/sign-in actually landed on the app, not
    // still on /setup or /login. The account's real id (needed to seed
    // runner/board data as its owner, before the scenario run below can
    // render anything real against it) lives in core's own access token, in
    // an in-page JS module variable out of reach of a plain fetch
    // (authClient.ts) — the Go orchestrator reads it back from core's own
    // database instead (ReadLocalAccountID), not from this page.
    const signedIn = !page.url().includes('/setup') && !page.url().includes('/login')
    if (!signedIn) {
      throw new Error(`account creation/sign-in did not land on the app: ${page.url()}`)
    }
    await writeFile(OUT, JSON.stringify({ username: USERNAME }, null, 2))
    await browser.close()
    return
  }

  // Land on the runner app once, for real, before any scenario — core's own
  // login page is a different origin, and nothing else here ever navigates
  // there first. Without this, insights-7d's pre hook (the first scenario
  // to run) waited on a sidebar workspace title that was never on the page,
  // since the whole point of MED a was never to leave this landing again
  // (Codex review of d7e50390): every repeat click after this one re-uses
  // the same authenticated session instead of re-navigating through core.
  await page.goto(RUNNER_URL)
  if (env('RELEASE_PERF_MODE') === 'boot-only') {
    // The historical sidebar predates the title tooltip, but displays the same seed title.
    try { await page.getByText('Release-perf workspace 0', { exact: true }).first().waitFor() }
    catch (error) {
      const state = await page.evaluate(() => ({ path: location.pathname, tabs: document.querySelectorAll('[role="tab"]').length, signIn: !!document.querySelector('input[type="password"]'), emptyApp: !document.querySelector('#root')?.childElementCount, notFound: document.body.textContent?.includes('Session not found') ?? false }))
      console.error('Historical boot setup failed', { cause: 'workspace_not_ready', ...state, browserErrorCount: pageErrors.length })
      throw error
    }
  } else await page.locator('[title="Release-perf workspace 0"]').waitFor()

  if (env('RELEASE_PERF_MODE') === 'boot-only') {
    try {
      const boot = await runBootTrials(page, context, { runnerURL: RUNNER_URL, longSession: LONG_SESSION_ID })
      await writeFile(OUT, JSON.stringify({ status: 'complete', boot }, null, 2), { mode: 0o600 })
    } finally { await browser.close() }
    return
  }

  const results = []
  try {
    results.push(await repeated(page, {
      name: 'insights-7d',
      markName: 'insights-rendered',
      // A page.goto here (every repetition) re-authenticates from scratch
      // every time: insights fires several parallel fetches on a cold
      // page with no access token yet, which can overwhelm the silent
      // in-page refresh and force a visible bounce through core — which
      // refreshGuard.ts bounds to MAX_ATTEMPTS=3 per WINDOW_MS=120s
      // (unrelated to BLG-487's Web Lock fix). Relying on running first,
      // before other reload-heavy scenarios spend that budget, was
      // itself an order dependency (Codex review of d7e50390, MED a).
      // The session is established once, by the account-only sign-in
      // (main.go); this scenario now never leaves it — it clicks the
      // real "Insights" nav button from an already-authenticated
      // session page, the same client-side navigation a person uses,
      // so no repetition ever needs to bounce through core at all. pre
      // leaves for a session first (untimed) so each repetition is a
      // real, fresh navigation into Insights, not a re-select of an
      // already-mounted, already-cached one. GlobalInsights is keyed by
      // identity and workspace, which don't change between clicks, so a
      // repeat click on an already-open Insights tab just re-focuses the
      // same mounted instance (no new mark) — and even closing and
      // reopening it only remounts against insightsCache.ts's in-memory
      // cache, which a tab close doesn't clear (unlike a real page
      // reload), so the "fresh" reopen would still be a cache hit, not a
      // real fetch. pre closes the tab, then clears that cache the same
      // way a real identity change already does (insightsCache.ts's own
      // 'blerg-identity-changed' listener), while untimed and before
      // action's click re-mounts it — so every repetition measures a real
      // fetch, the same way the first one did.
      pre: async () => {
        await page.locator('[title="Release-perf workspace 0"]').click()
        await page.getByRole('button', { name: 'Close Insights' }).click({ timeout: 2000 }).catch(() => {})
        await page.evaluate(() => window.dispatchEvent(new Event('blerg-identity-changed')))
      },
      action: async () => {
        // The sidebar's "Insights" nav item is a role="link" div
        // (SessionList.tsx), not a button — getByRole('button', {name:
        // 'Insights'}) never matched it at all and instead case-insensitive-
        // substring-matched the unrelated "Actions for Release-perf
        // insights owner" kebab menu button, whose click opens nothing (a
        // no-op menu for a different seeded workspace), silently stranding
        // every repetition until the mark timeout. Found by actually
        // running this scenario for the first time after the MED a
        // rewrite.
        await page.getByRole('link', { name: 'Insights' }).click()
        // Asserts the "no repetition bounces through core" claim above,
        // rather than just hoping it stays true: a real bounce shows up
        // as a navigation to CORE_URL's own origin.
        if (navigations.some(n => n.url.startsWith(CORE_URL))) {
          throw new Error(`insights-7d bounced through core (${CORE_URL}) on a repeated in-app click — it should never need to`)
        }
      },
      serverTimingLog,
      serverEndpoint: '/api/insights?',
    }, REPS))
    if (LONG_SESSION_ID) {
      // BLG-491 (filed from this card's own finding): a fresh page load's
      // WebSocket pays sendInitialState's full account scan on every
      // connect, not just the first ever one — every reload, refresh and
      // phone reconnect. A reload-driven scenario measures that real,
      // connect-time cost on purpose, as its own named thing, separate
      // from in-app navigation (workspace-switch, below) which never pays
      // it once the socket is already open.
      results.push(await repeated(page, {
        name: 'cold-load',
        markName: 'transcript-rendered',
        // Which tab was last active for a session is persisted and
        // survives a reload — insights-7d (just above) can leave this
        // session on its own Insights tab, active, so the reloaded page
        // would land back on Insights, chat pane hidden, and
        // transcript-rendered would never fire. Select Main first and
        // wait for it to really take, the same race transcript-open
        // guards against below.
        pre: async () => {
          await page.getByRole('tab', { name: 'Main' }).click({ timeout: 2000 }).catch(() => {})
          await page.waitForFunction(
            () => document.querySelector('[role="tab"][aria-selected="true"]')?.textContent?.includes('Main'),
            null, { timeout: 2000 },
          ).catch(() => {})
        },
        action: async () => { await page.goto(RUNNER_URL + '/sessions/' + LONG_SESSION_ID) },
        serverTimingLog,
        // No serverEndpoint: the real connect-time cost this scenario
        // measures (BLG-491's sendInitialState) is pushed over the
        // WebSocket, never a REST response this Server-Timing mechanism
        // can see — reporting a server number here would just be
        // whichever unrelated request happened to carry the header.
      }, REPS))
    }
    if (LONG_SESSION_ID && SWITCH_TARGET_ID) {
      // A page.goto for every repetition measured a full reload's cost, not
      // switching workspaces: the client total included the same fresh-
      // WebSocket cost cold-load (above) measures on purpose — a real
      // workspace switch click never pays it, since the socket from the
      // page's own first load stays open across in-app navigation.
      // Alternate real sidebar clicks between the two seeded workspace
      // titles (seed.go: "Release-perf workspace 0"/"1" for the long-
      // transcript and switch-target sessions) instead.
      let onTarget = false
      results.push(await repeated(page, {
        name: 'workspace-switch',
        markName: 'workspace-switched',
        pre: () => ensureWorkspace1Visible(page),
        action: async () => {
          onTarget = !onTarget
          await page.locator(onTarget ? '[title="Release-perf workspace 1"]' : '[title="Release-perf workspace 0"]').click()
        },
        serverTimingLog,
        // No serverEndpoint: the switched-to session's own data resolves
        // from the already-loaded store (the WebSocket's initial_state),
        // not a fresh REST fetch this click triggers.
      }, REPS))
    }
    results.push(await repeated(page, {
      name: 'search',
      markName: 'search-results-rendered',
      action: async () => {
        await page.keyboard.press('Meta+k').catch(() => {})
        await page.keyboard.press('Control+k').catch(() => {})
        await page.keyboard.type('release-perf')
      },
      serverTimingLog,
      // No serverEndpoint: CommandPalette filters the already-loaded
      // session list client-side — there's no backend call to match.
    }, REPS))
    if (BOARD_ID) {
      results.push(await repeated(page, {
        name: 'board-tab',
        markName: 'board-rendered',
        action: async () => { await page.goto(RUNNER_URL + '/boards/' + BOARD_ID) },
        serverTimingLog,
        serverEndpoint: '/api/boards/' + BOARD_ID,
      }, REPS))
    }
    if (LONG_SESSION_ID) {
      // Same reasoning as workspace-switch: a page.goto here measured a
      // fresh WebSocket's initial_state cost (1.7-2.2s against this seed's
      // 446 sessions), not opening a transcript. pre (untimed) leaves for
      // the switch-target workspace first so the timed action is a real
      // click back into the long-transcript one, same as a person
      // clicking it from the sidebar — unless SWITCH_TARGET_ID is absent,
      // where a reload is the only way left to re-trigger the mark.
      results.push(await repeated(page, {
        name: 'transcript-open',
        markName: 'transcript-rendered',
        // insights-7d (just above, in scenario order) can leave
        // workspace 0 on its own Insights tab, active — and each
        // workspace remembers its own last-active tab across a switch
        // away and back, so returning to workspace 0 would show the
        // cached Insights tab, Activity-hidden chat pane and all,
        // instead of the transcript, and transcript-rendered would
        // never fire. Select the "Main" chat tab explicitly, while
        // still on workspace 0 (Insights' own tab only exists in its
        // currently-shown SessionDetail) — not relying on whatever
        // closing it happens to leave active.
        pre: SWITCH_TARGET_ID ? async () => {
          await page.getByRole('tab', { name: 'Main' }).click({ timeout: 2000 }).catch(() => {})
          // The click's own event dispatch resolves before React commits
          // and persists "chat is the active tab" — switching workspaces
          // immediately after raced that persist and silently lost it, so
          // workspace 0 came back on Insights instead. Wait for the real
          // effect, not just the click.
          await page.waitForFunction(
            () => document.querySelector('[role="tab"][aria-selected="true"]')?.textContent?.includes('Main'),
            null, { timeout: 2000 },
          ).catch(() => {})
          await ensureWorkspace1Visible(page)
          const since = await page.evaluate(() => Date.now())
          await page.locator('[title="Release-perf workspace 1"]').click()
          // The route click can resolve before React commits the switch. A
          // second click that wins that race just reselects workspace 0 and
          // cannot emit another transcript-rendered mark.
          await waitForMark(page, 'workspace-switched', since)
          await resetRetainedViews(page)
        } : undefined,
        action: async () => {
          if (SWITCH_TARGET_ID) await page.locator('[title="Release-perf workspace 0"]').click()
          else await page.goto(RUNNER_URL + '/sessions/' + LONG_SESSION_ID)
        },
        serverTimingLog,
        // No serverEndpoint: the long-transcript session's events arrive
        // over the WebSocket (same as workspace-switch), not a REST
        // response.
      }, REPS))
      results.push(await repeated(page, {
        name: 'typing',
        markName: 'key-committed',
        action: async () => { await page.locator('.agent-composer textarea').first().press('x'); },
        serverTimingLog,
        // No serverEndpoint: a keystroke is a pure client-side state
        // update — there's no backend call for it to match at all.
      }, REPS))
      results.push(await repeated(page, {
        name: 'blackboard',
        markName: 'blackboard-rendered',
        // Close the actual workspace tab, then invalidate retained view data.
        // Finish the socket reconnect before timing the next fresh open.
        pre: async () => {
          await closeBlackboardTab(page)
          await resetRetainedViews(page)
        },
        action: async () => { await page.getByRole('button', { name: /blackboard/i }).first().click() },
        serverTimingLog,
        serverEndpoint: '/api/blackboards/',
      }, REPS))
    }
    if (Number(env('RELEASE_PERF_SOAK_SECONDS', '0')) > 0) {
      await runAcceptance(page, context, {
        seconds: Number(env('RELEASE_PERF_SOAK_SECONDS')), intervalSeconds: 30,
        pids: JSON.parse(env('RELEASE_PERF_PROCESS_PIDS', '{}')),
        output: env('RELEASE_PERF_ACCEPTANCE_OUT'), runnerURL: RUNNER_URL,
        longSession: LONG_SESSION_ID,
      })
    }
  } finally {
    await writeFile(OUT, JSON.stringify({ scenarios: results }, null, 2))
    await browser.close()
  }
}

main().catch(err => { console.error(err); process.exit(1) })
