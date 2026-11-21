import { browserFixtureBinary } from './browserFixtureBinary'
import { spawn } from 'node:child_process'
import { watch } from 'node:fs'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

interface Fixture { url: string; token: string; sessionId: string }
const fixtures = new Map<string, () => Promise<void>>()
export async function startDatasetFixture(_context: unknown, origin: string, mcp = false): Promise<Fixture> {
  const binary = await browserFixtureBinary()
  const dir = await mkdtemp(join(tmpdir(), 'blerg-dataset-'))
  const path = join(dir, 'runner.json')
  let diagnostics = ''
  let rejectReady: (error: Error) => void = () => {}
  let cleanupReady = () => {}
  const ready = new Promise<Fixture>((resolveReady, reject) => {
    rejectReady = reject
    const watcher = watch(dir, async (_event, name) => {
      if (name !== 'runner.json') return
      try { const value = JSON.parse(await readFile(path, 'utf8')) as Fixture; watcher.close(); resolveReady(value) }
      catch { /* The atomic fixture write may still be in flight. */ }
    })
    const timer = setTimeout(() => { watcher.close(); reject(new Error('Dataset fixture timed out: ' + diagnostics)) }, 90000)
    timer.unref()
    cleanupReady = () => { clearTimeout(timer); watcher.close() }
    // Stop watching on both successful readiness and child startup failures.
    rejectReady = error => { cleanupReady(); reject(error) }
  })
  const child = spawn(binary, ['-test.run', '^TestDatasetBrowserFixture$', '-test.timeout', '5m'], {
    cwd: resolve('..', 'internal', 'server'), env: { ...process.env, GOWORK: 'off', GOFLAGS: process.env.GOFLAGS || '-p=' + (process.env.BLERG_TOOL_WORKERS || '2'), BLERG_DATASET_FIXTURE: path, BLERG_DATASET_MCP: mcp ? '1' : '', BLERG_RUNNER_ALLOWED_ORIGINS: origin },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  child.stdout?.on('data', chunk => { diagnostics += String(chunk) })
  child.stderr?.on('data', chunk => { diagnostics += String(chunk) })
  child.once('error', rejectReady)
  const ended = new Promise<number | null>(resolveEnd => child.once('exit', code => { rejectReady(new Error('Dataset fixture exited: ' + diagnostics)); resolveEnd(code) }))
  try {
    const fixture = await ready
    cleanupReady()
    fixtures.set(fixture.token, async () => {
      await fetch(fixture.url + '/fixture/stop', { method: 'POST', headers: { Authorization: 'Bearer ' + fixture.token } })
      const code = await ended
      await rm(dir, { recursive: true, force: true })
      if (code !== 0) throw new Error('Dataset fixture failed: ' + diagnostics)
    })
    return fixture
  } catch (error) { cleanupReady(); child.kill('SIGTERM'); await ended; await rm(dir, { recursive: true, force: true }); throw error }
}
export async function stopDatasetFixture(_context: unknown, token: string) {
  const stop = fixtures.get(token)
  fixtures.delete(token)
  await stop?.()
}
