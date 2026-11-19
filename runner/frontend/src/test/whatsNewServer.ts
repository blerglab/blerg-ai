import { browserFixtureBinary } from './browserFixtureBinary'
// Node-side browser fixture. The browser calls the same-origin runner route;
// this middleware forwards it to a real runner backed by real core and Postgres.
import { spawn } from 'node:child_process'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { request } from 'node:http'
import type { Plugin } from 'vite'

interface Fixture { url: string; token: string }
const fixtures = new Map<string, Fixture & {stop: () => Promise<void>}>()
export async function startWhatsNewFixture(): Promise<Fixture> {
  const binary=await browserFixtureBinary()
  const dir=await mkdtemp(join(tmpdir(),'blerg-whats-new-'))
  const path=join(dir,'runner.json')
  const child=spawn(binary,['-test.run','^TestWhatsNewBrowserHTTPFixture$','-test.timeout','5m'],{
    cwd:resolve('..','internal','server'),
    env:{...process.env,GOWORK:'off',GOFLAGS:process.env.GOFLAGS || '-p='+(process.env.BLERG_TOOL_WORKERS || '2'),BLERG_WHATS_NEW_RUNNER_FIXTURE:path},
    stdio:['ignore','pipe','pipe'],
  })
  let diagnostics=''
  child.stdout.on('data',chunk => {diagnostics+=String(chunk)})
  child.stderr.on('data',chunk => {diagnostics+=String(chunk)})
  const ended=new Promise<number|null>(resolve => child.once('exit',resolve))
  for (let i=0;i<1200;i++) {
    try {
      const fixture=JSON.parse(await readFile(path,'utf8')) as Fixture
      fixtures.set(fixture.token,{...fixture,stop:async () => {
        await writeFile(path+'.stop','')
        let timer:ReturnType<typeof setTimeout>|undefined
        const result=await Promise.race([ended,new Promise<'timeout'>(resolve => {timer=setTimeout(() => resolve('timeout'),15000)})])
        clearTimeout(timer)
        if (result==='timeout') child.kill('SIGTERM')
        await rm(dir,{recursive:true,force:true})
        if (result!==0) throw new Error('Backend fixture failed: '+diagnostics)
      }})
      return fixture
    } catch { /* Wait for the private configuration file, without logging its token. */ }
    if (child.exitCode!==null) throw new Error('Backend fixture failed: '+diagnostics)
    await new Promise(resolve => setTimeout(resolve,50))
  }
  child.kill('SIGTERM')
  throw new Error('Backend fixture did not start: '+diagnostics)
}
export async function stopWhatsNewFixture(_context: unknown, token:string) {
  const fixture=fixtures.get(token)
  if (!fixture) return
  fixtures.delete(token)
  await fixture.stop()
}
export function whatsNewBackend(): Plugin {
  return {
    name:'whats-new-real-backend',
    configureServer(server) {
      server.middlewares.use((req,res,next) => {
        if (!req.url?.startsWith('/api/me/whats-new')) {next();return}
        const token=req.headers.authorization?.replace(/^Bearer /,'') ?? ''
        const fixture=fixtures.get(token)
        if (!fixture) {next();return}
        const upstream=request(new URL(req.url,fixture.url),{method:req.method,headers:{authorization:req.headers.authorization}},response => {
          res.writeHead(response.statusCode ?? 503,response.headers)
          response.pipe(res)
        })
        upstream.on('error',() => {res.statusCode=503;res.end('Backend unavailable')})
        req.pipe(upstream)
      })
      server.httpServer?.once('close',() => {
        for (const fixture of fixtures.values()) void fixture.stop()
        fixtures.clear()
      })
    },
  }
}
