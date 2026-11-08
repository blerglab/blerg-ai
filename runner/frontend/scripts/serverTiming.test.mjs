import test from 'node:test'
import assert from 'node:assert/strict'
import { pickServerTiming } from './serverTiming.mjs'

test('no endpoint means no server number, not a guess', () => {
  assert.equal(pickServerTiming([{ url: '/api/insights?range=7d', ms: 12 }], undefined), null)
})

test('no logged response matches the endpoint', () => {
  assert.equal(pickServerTiming([{ url: '/api/boards/abc', ms: 8 }], '/api/insights?'), null)
})

test('matches the scenario endpoint, ignoring an unrelated later response', () => {
  const log = [
    { url: '/api/insights?range=7d', ms: 12 },
    { url: '/api/boards/abc', ms: 400 },
  ]
  assert.equal(pickServerTiming(log, '/api/insights?'), 12)
})

test('takes the last matching response when the endpoint is hit more than once', () => {
  const log = [
    { url: '/api/boards/abc', ms: 5 },
    { url: '/api/boards/abc', ms: 9 },
  ]
  assert.equal(pickServerTiming(log, '/api/boards/abc'), 9)
})
