// BLG-453 (Codex review of d7e50390, HIGH 2): a mark-timeout or action
// failure's debug payload used to include the full navigation URL,
// fragment and all — including #access_token=... after a core refresh
// redirect. Run with: node --test scripts/redact.test.mjs
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { redactUrl } from './redact.mjs'

test('strips the fragment entirely, token and all', () => {
  const raw = 'http://127.0.0.1:5173/sessions/abc#blerg_refresh_return#access_token=eyJhbGciOiJFZERTQSJ9.payload.sig'
  const out = redactUrl(raw)
  assert.ok(!out.includes('access_token'), `token leaked: ${out}`)
  assert.ok(!out.includes('eyJhbGciOiJFZERTQSJ9'), `token payload leaked: ${out}`)
  assert.ok(!out.includes('#'), `fragment not stripped: ${out}`)
})

test('redacts a token/code/secret/password/auth query param, keeps the rest of the URL readable', () => {
  const out = redactUrl('http://127.0.0.1:5173/auth/refresh?return_to=http%3A%2F%2Fx&code=abc123&other=keep-me')
  assert.ok(!out.includes('abc123'), `code value leaked: ${out}`)
  assert.ok(out.includes('return_to='), `unrelated param dropped: ${out}`)
  assert.ok(out.includes('other=keep-me'), `unrelated param dropped: ${out}`)
})

test('is case-insensitive and matches a param name containing the word, not just an exact match', () => {
  const out = redactUrl('http://x/y?Access_Token=secretvalue&authToken=anothersecret')
  assert.ok(!out.includes('secretvalue'), `leaked: ${out}`)
  assert.ok(!out.includes('anothersecret'), `leaked: ${out}`)
})

test('never throws on an unparseable URL, returns a safe placeholder instead', () => {
  assert.doesNotThrow(() => redactUrl('not a url'))
  assert.equal(redactUrl('not a url'), '[unparseable url]')
})

test('leaves an ordinary URL with no secrets and no fragment unchanged in substance', () => {
  const out = redactUrl('http://127.0.0.1:5173/sessions/abc')
  assert.ok(out.includes('/sessions/abc'), out)
  assert.ok(!out.includes('#'), out)
})

// release-perf-playwright.mjs's own waitForMark/measure build their thrown
// error message as `...; debug: ${JSON.stringify(debug)}; ...` with
// debug.url set from redactUrl(location.href) — this proves that exact
// shape, with a real-looking bearer token in the URL fragment, never
// surfaces in the resulting string (the thing a mark timeout or action
// failure actually writes to the evidence dir's log file).
test('a token-bearing URL never survives into the error message a mark timeout or action failure throws', () => {
  const token = 'eyJhbGciOiJFZERTQSIsImtpZCI6InZJd1pocGloYk9JIn0.eyJzdWIiOiJub3QtcmVhbCJ9.fakesig'
  const debug = {
    url: redactUrl(`http://127.0.0.1:5173/sessions/abc#blerg_refresh_return#access_token=${token}`),
    buttons: ['Open blackboard'],
    marks: ['workspace-switched'],
  }
  const message = `mark transcript-rendered never appeared within 15000ms; debug: ${JSON.stringify(debug)}; pageErrors: []; navigations: []`
  assert.ok(!message.includes(token), `token leaked into the thrown error message: ${message}`)
  assert.ok(!message.includes('access_token'), `fragment key leaked into the thrown error message: ${message}`)
})
