import type {BlergSocket} from './blerg'
import {mcpSampleExpiry} from '../visual/datasets'
import type {DatasetReference, DatasetRow, DatasetSample} from '../visual/datasets'

/** One bounded keyed store per authorized subscription. A gap requests a fresh
 * snapshot, never applies a changeset against the wrong base. */
export function subscribeDataset(socket: BlergSocket, sessionId: string, data: DatasetReference, receive: (sample: DatasetSample & {rows: DatasetRow[]}) => void): () => void {
  let id = crypto.randomUUID()
  let epoch = '', sequence = 0, alive = true, observedAt = ''
  const rows = new Map<string, DatasetRow>()
  let validUntil = '', expiryTimer: ReturnType<typeof setTimeout> | undefined
  const send = (type: 'subscribe_dataset' | 'unsubscribe_dataset' | 'dataset_heartbeat') =>
    socket.send({type, session_id: sessionId, subscription_id: id, data})
  const cancelExpiry = () => {clearTimeout(expiryTimer); expiryTimer = undefined}
  const expire = () => {
    cancelExpiry(); rows.clear()
    console.warn('dataset_failed', {cause: 'sample_expired'})
    socket.send({type: 'dataset_diagnostic', session_id: sessionId, subscription_id: id, data, cause: 'sample_expired'})
    receive({type: 'dataset_delta', subscription_id: id, epoch, base_sequence: sequence, sequence, snapshot: true, upserts: [], removals: [], observed_at: observedAt, valid_until: validUntil, status: 'access_removed', cause: 'sample_expired', rows: []})
  }
  const clear = (status: 'disconnected' | 'access_removed') => {
    if (status === 'access_removed') {rows.clear(); cancelExpiry()}
    receive({type: 'dataset_delta', subscription_id: id, epoch, base_sequence: sequence, sequence, snapshot: false, upserts: [], removals: [], observed_at: observedAt, valid_until: validUntil, status, rows: [...rows.values()]})
  }
  const restart = () => {
    send('unsubscribe_dataset')
    id = crypto.randomUUID(); epoch = ''; sequence = 0
    rows.clear(); cancelExpiry(); validUntil = ''
    send('subscribe_dataset')
  }
  const offMessage = socket.onMessage('dataset_delta', (sample: DatasetSample) => {
    if (!alive || sample.subscription_id !== id) return
    if (sample.epoch === epoch && sample.sequence <= sequence) return
    if (epoch && sample.epoch !== epoch) return
    if (sample.status === 'access_removed') {
      epoch = sample.epoch; sequence = sample.sequence
      rows.clear(); cancelExpiry(); receive({...sample, rows: []}); return
    }
    if (!sample.snapshot && (sample.epoch !== epoch || sample.base_sequence !== sequence)) {
      console.warn('dataset_failed', {cause: 'sequence_gap'})
      socket.send({type: 'dataset_diagnostic', session_id: sessionId, subscription_id: id, data, cause: 'sequence_gap'})
      restart()
      return
    }
    if (!Number.isSafeInteger(sample.sequence) || sample.sequence < 1 || !Array.isArray(sample.upserts) || sample.upserts.length > 10_000 || !Array.isArray(sample.removals) || sample.removals.length > 10_000
      || sample.removals.some(key => typeof key !== 'string' || key.length > 128)
      || sample.upserts.some(row => !row || typeof row !== 'object' || typeof row.key !== 'string' || row.key.length > 128 || Object.keys(row).length > 16
        || Object.values(row).some(value => !(value === null || typeof value === 'boolean' || (typeof value === 'number' && Number.isFinite(value)) || (typeof value === 'string' && value.length <= 256))))) {
      console.warn('dataset_failed', {cause: 'invalid_sample'}); socket.send({type: 'dataset_diagnostic', session_id: sessionId, subscription_id: id, data, cause: 'invalid_sample'}); return
    }
    const next = sample.snapshot ? new Map<string, DatasetRow>() : new Map(rows)
    for (const key of sample.removals) next.delete(key)
    for (const row of sample.upserts) next.set(String(row.key), row)
    if (next.size > 10_000) {
      console.warn('dataset_failed', {cause: 'invalid_sample'})
      socket.send({type: 'dataset_diagnostic', session_id: sessionId, subscription_id: id, data, cause: 'invalid_sample'})
      return
    }
    if (data.name === 'blerg.mcp') {
      const deadline = mcpSampleExpiry(sample)
      // An unavailable empty heartbeat may preserve the existing bounded store;
      // it cannot install a new value or extend the old deadline.
      const retained = !sample.snapshot && sample.upserts.length === 0 && sample.status === 'unavailable' && mcpSampleExpiry({observed_at: observedAt, valid_until: validUntil}) > Date.now()
      if (deadline <= Date.now() && !retained) {
        if (next.size === 0 && sample.status === 'unavailable') {cancelExpiry(); validUntil = ''}
        else {expire(); return}
      }
      if (deadline > Date.now()) {
        validUntil = new Date(deadline).toISOString()
        cancelExpiry(); expiryTimer = setTimeout(expire, deadline - Date.now())
      }
    }
    rows.clear()
    for (const [key, row] of next) rows.set(key, row)
    epoch = sample.epoch; sequence = sample.sequence; observedAt = sample.observed_at
    receive({...sample, ...(data.name === 'blerg.mcp' ? {valid_until: validUntil} : {}), rows: [...rows.values()].sort((a, b) => (data.name === 'blerg.gates.durations' ? String(a.end).localeCompare(String(b.end)) : 0) || String(a.key).localeCompare(String(b.key)))})
  })
  const offClose = socket.onClose?.(() => clear('disconnected'))
  let opened = false
  const offOpen = socket.onOpen(() => {
    if (opened) restart()
    else {opened = true; send('subscribe_dataset')}
  })
  const heartbeat = setInterval(() => send('dataset_heartbeat'), 30_000)
  return () => {
    alive = false
    clearInterval(heartbeat); cancelExpiry()
    send('unsubscribe_dataset')
    offMessage(); offClose?.(); offOpen()
    rows.clear()
  }
}
