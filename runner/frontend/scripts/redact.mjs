// BLG-453 (Codex review of d7e50390, HIGH 2): release-perf-playwright.mjs
// logs page URLs for diagnosis — mark-timeout and action-failure debug
// payloads, the navigation history, the DIAGNOSE breakdown. A real
// navigation URL can carry a bearer token in its fragment (core's
// /auth/refresh redirect appends #access_token=...) or in a query param
// (code, token, secret, password, auth). redactUrl strips the fragment
// entirely and masks any query param whose name contains one of those
// words, so a captured URL is safe to write to a log file or a published
// report.
const SECRET_PARAM = /token|secret|code|password|auth/i

export function redactUrl(raw) {
  let u
  try {
    u = new URL(raw)
  } catch {
    return '[unparseable url]'
  }
  u.hash = ''
  for (const key of [...u.searchParams.keys()]) {
    if (SECRET_PARAM.test(key)) u.searchParams.set(key, '[redacted]')
  }
  return u.toString()
}
