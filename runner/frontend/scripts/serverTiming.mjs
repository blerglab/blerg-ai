// BLG-453 (Codex review of d7e50390, MED d): captureServerTiming logs every
// Server-Timing-bearing response the whole page makes during a
// measurement window, in whatever order they land — a scenario that
// makes several requests, or none of its own, used to just take "the last
// one," silently reporting an unrelated background request's timing.
// pickServerTiming matches a scenario's own endpoint explicitly; a
// scenario with no real REST endpoint (client-side only, or whose data
// arrives over the WebSocket instead) passes no endpoint and gets no
// server number, honestly, instead of a guess.
export function pickServerTiming(log, endpoint) {
  if (!endpoint) return null
  const matching = log.filter(e => e.url.includes(endpoint))
  return matching.length ? matching[matching.length - 1].ms : null
}
