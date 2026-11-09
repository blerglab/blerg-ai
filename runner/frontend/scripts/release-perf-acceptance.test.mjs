import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { processSample, growth, typingMeasurement } from './release-perf-acceptance.mjs'

test('typing gates keydown to commit while retaining the driver delay separately', () => {
 assert.deepEqual(typingMeasurement(1000, 1031, 1032), { clientMs: 1, preKeydownMs: 31, commandToCommitMs: 32 })
 for (const values of [[1000,null,1032],[1000,NaN,1032],[1000,999,1032],[1000,1032,1031]]) assert.throws(()=>typingMeasurement(...values),/invalid_typing_timestamps/)
})

test('process samples bind PID lifetime and expose only numeric memory/thread data', () => {
 const root=mkdtempSync(join(tmpdir(),'acceptance-proc-'))
 try {
  mkdirSync(join(root,'42','fd'),{recursive:true})
  writeFileSync(join(root,'42','status'),'Name:\tfixture\nVmRSS:\t100 kB\nThreads:\t3\n')
  writeFileSync(join(root,'42','stat'),'42 (fixture with spaces) S '+Array.from({length:20},(_,i)=>i===18?'999':'0').join(' '))
  const sample=processSample(42,root)
  assert.equal(sample.rssBytes,102400)
  assert.equal(sample.threads,3)
  assert.equal(sample.startTicks,'999')
  assert.equal(sample.fdCount,0)
  assert.throws(()=>processSample(42,root,'different'),/process_replaced/)
 } finally {rmSync(root,{recursive:true,force:true})}
})
test('growth uses a window median, preserving transient maxima separately', () => {
 const samples=[10,10,1000,12,12,12].map((value,i)=>({atSeconds:i,rssBytes:value}))
 assert.equal(growth(samples,'rssBytes').delta,2)
 assert.equal(growth(samples,'rssBytes').maximum,1000)
})
