import { afterEach, beforeEach, expect, it, vi } from 'vitest'
const mocks = vi.hoisted(() => ({ exec: vi.fn(), directory: vi.fn(), remove: vi.fn() }))
vi.mock('node:child_process', () => ({ default: {execFile: mocks.exec}, execFile: mocks.exec }))
vi.mock('node:fs/promises', () => ({ default: {mkdtemp: mocks.directory}, mkdtemp: mocks.directory }))
vi.mock('node:fs', () => ({ default: {rmSync: mocks.remove}, rmSync: mocks.remove }))
afterEach(() => vi.unstubAllEnvs())
beforeEach(() => {
 vi.stubEnv('BLERG_TEST_GO', ''); vi.resetModules(); mocks.exec.mockReset(); mocks.directory.mockReset().mockResolvedValue('/tmp/example-browser-fixture')
})
it('shares one bounded Go compilation across concurrent and later HTTP fixture starts', async () => {
 const { browserFixtureBinary } = await import('./browserFixtureBinary')
 const first = browserFixtureBinary(), second = browserFixtureBinary()
 await vi.waitFor(() => expect(mocks.exec).toHaveBeenCalled())
 expect(mocks.exec).toHaveBeenCalledTimes(1)
 const [go, args, options, done] = mocks.exec.mock.calls[0]
 expect(go).toBe('go')
 expect(args).toEqual(['test', '-c', '-o', '/tmp/example-browser-fixture/runner.test', './internal/server'])
 expect(options.timeout).toBe(90000)
 expect(options.env.GOWORK).toBe('off')
 expect(options.env.GOFLAGS).toMatch(/-p=/)
 done(null)
 expect(await first).toBe(await second)
 expect(await browserFixtureBinary()).toBe('/tmp/example-browser-fixture/runner.test')
 expect(mocks.exec).toHaveBeenCalledTimes(1)
})
it('reports a build failure without exposing compiler output or environment values', async () => {
 const warn = vi.spyOn(console, 'error').mockImplementation(() => {})
 const { browserFixtureBinary } = await import('./browserFixtureBinary')
 const pending = browserFixtureBinary()
 const rejected = expect(pending).rejects.toThrow('Browser fixture compilation failed')
 await vi.waitFor(() => expect(mocks.exec).toHaveBeenCalled())
 mocks.exec.mock.calls[0][3](Object.assign(new Error('canary-private-output'), {code: 1}))
 await rejected
 expect(warn).toHaveBeenCalledWith('Browser fixture compilation failed', {cause: 'command_failed', code: 1, killed: false, signal: null})
 expect(JSON.stringify(warn.mock.calls)).not.toContain('canary-private-output')
 warn.mockRestore()
})

it('honors an explicit Go toolchain override', async () => {
 vi.stubEnv('BLERG_TEST_GO', '/toolchain/bin/go')
 const { browserFixtureBinary } = await import('./browserFixtureBinary')
 const pending = browserFixtureBinary()
 await vi.waitFor(() => expect(mocks.exec).toHaveBeenCalled())
 expect(mocks.exec.mock.calls[0][0]).toBe('/toolchain/bin/go')
 mocks.exec.mock.calls[0][3](null)
 await pending
})

it('distinguishes a terminated compilation without exposing command diagnostics', async () => {
 const warn = vi.spyOn(console, 'error').mockImplementation(() => {})
 const { browserFixtureBinary } = await import('./browserFixtureBinary')
 const pending = browserFixtureBinary()
 const rejected = expect(pending).rejects.toThrow('Browser fixture compilation failed')
 await vi.waitFor(() => expect(mocks.exec).toHaveBeenCalled())
 mocks.exec.mock.calls[0][3](Object.assign(new Error('canary-private-command'), {code: null, killed: true, signal: 'SIGTERM'}))
 await rejected
 expect(warn).toHaveBeenCalledWith('Browser fixture compilation failed', {cause: 'command_failed', code: null, killed: true, signal: 'SIGTERM'})
 expect(JSON.stringify(warn.mock.calls)).not.toContain('canary-private-command')
 warn.mockRestore()
})
