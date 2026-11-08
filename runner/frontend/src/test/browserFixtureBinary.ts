import { execFile } from 'node:child_process'
import { rmSync } from 'node:fs'
import { mkdtemp } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

// Compile once for this Vitest process. Each fixture still runs in a separate process
// and owns an isolated database; concurrent starts do not contend in repeated Go builds.
let binary: Promise<string> | undefined
export function browserFixtureBinary(): Promise<string> {
 return binary ??= build()
}
async function build(): Promise<string> {
 const directory = await mkdtemp(join(tmpdir(), 'blerg-browser-fixture-'))
 process.once('exit', () => { rmSync(directory, {recursive: true, force: true}) })
 const path = join(directory, 'runner.test')
 await new Promise<void>((done, reject) => {
  execFile(process.env.BLERG_TEST_GO || 'go', ['test', '-c', '-o', path, './internal/server'], {
   timeout: 90000, cwd: resolve('..'), env: {...process.env, GOWORK: 'off', GOFLAGS: process.env.GOFLAGS || '-p=' + (process.env.BLERG_TOOL_WORKERS || '2')},
  }, error => {
   if (!error) { done(); return }
   console.error('Browser fixture compilation failed', {cause: 'command_failed', code: error.code, killed: error.killed === true, signal: error.signal ?? null})
   reject(new Error('Browser fixture compilation failed'))
  })
 })
 return path
}
