import {useEffect, useRef, useState} from 'react'
import {mcpSampleExpiry} from '../visual/datasets'
import type {DatasetRow, DatasetSample} from '../visual/datasets'
import type {DashboardCard} from '../visual/dashboard'
import type {TransportFiles} from '../transport/types'

export function useDashboardDataset(card: DashboardCard, context?: {sessionId: string; files: TransportFiles}) {
  const ref = useRef<HTMLElement>(null)
  const [received, setReceived] = useState<(DatasetSample & {rows: DatasetRow[]; binding: string; sessionId: string}) | null>(null)
  const [now, setNow] = useState(Date.now)
  const unsupportedMCP = card.data.kind === 'dataset' && card.data.name === 'blerg.mcp' && ((card.kind !== 'stat' && card.kind !== 'gauge') || !!card.view?.sparkline)
  const binding = JSON.stringify(card.data.kind === 'dataset' ? card.data : null)
  const sample = received?.binding === binding && received?.sessionId === context?.sessionId ? received : null
  useEffect(() => {
    if (binding === 'null' || unsupportedMCP) return
    const data = JSON.parse(binding) as Extract<DashboardCard['data'], {kind: 'dataset'}>
    const element = ref.current
    if (!element || !context?.files.subscribeDataset) return
    const invalidate = context.files.onDatasetInvalidated?.(() => setReceived(null))
    let off: (() => void) | undefined, visible = false
    const update = () => {
      const active = visible && document.visibilityState !== 'hidden' && !element.closest('[hidden]')
      if (active && !off) off = context.files.subscribeDataset!(context.sessionId, data, sample => {
        setNow(Date.now())
        const expired = data.name === 'blerg.mcp' && sample.rows.length > 0 && mcpSampleExpiry(sample) <= Date.now()
        if (expired) {
          console.warn('dataset_failed', {cause: 'sample_expired'})
          context.files.reportVisualFailure?.(context.sessionId, 'sample_expired')
        }
        const incoming = expired ? {...sample, rows: [], upserts: [], removals: [], status: 'access_removed' as const, cause: 'sample_expired'} : sample
        setReceived(previous => {
          const prior = previous?.binding === binding && previous.sessionId === context.sessionId ? previous : null
          return {...incoming, binding, sessionId: context.sessionId,
            ...(sample.status === 'unavailable' && sample.rows.length === 0 && prior && data.name !== 'blerg.mcp' ? {rows: prior.rows, observed_at: prior.observed_at} : {})}
        })
      })
      else if (!active && off) { off(); off = undefined }
    }
    const observer = new IntersectionObserver(entries => { visible = entries.some(e => e.isIntersecting); update() })
    const mutations = new MutationObserver(update)
    observer.observe(element)
    mutations.observe(element.ownerDocument.body, {subtree: true, attributes: true, attributeFilter: ['hidden']})
    document.addEventListener('visibilitychange', update)
    const timer = setInterval(() => setNow(Date.now()), 15_000)
    return () => { invalidate?.(); off?.(); observer.disconnect(); mutations.disconnect(); clearInterval(timer); document.removeEventListener('visibilitychange', update) }
  }, [binding, unsupportedMCP, context?.sessionId, context?.files])
  const reportFailure = context?.files.reportVisualFailure, reportSession = context?.sessionId
  const mcp = card.data.kind === 'dataset' && card.data.name === 'blerg.mcp'
  useEffect(() => {
    if (!mcp || !sample || sample.status === 'access_removed' || (sample.rows.length === 0 && sample.status === 'unavailable')) return
    const deadline = mcpSampleExpiry(sample)
    const purge = () => {
      console.warn('dataset_failed', {cause: 'sample_expired'})
      if (reportSession) reportFailure?.(reportSession, 'sample_expired')
      setReceived(previous => previous === sample ? {...previous, rows: [], upserts: [], removals: [], status: 'access_removed', cause: 'sample_expired'} : previous)
    }
    if (deadline <= Date.now()) {purge(); return}
    const timer = setTimeout(purge, deadline - Date.now())
    return () => clearTimeout(timer)
  }, [mcp, sample, reportFailure, reportSession])
  // A binding can be selected again before the display clock ticks or effects purge.
  // eslint-disable-next-line react-hooks/purity -- Every render must recheck the absolute authority deadline.
  const expired = mcp && sample && sample.status !== 'access_removed' && sample.rows.length > 0 && mcpSampleExpiry(sample) <= Date.now()
  const rows = card.data.kind === 'inline' ? card.data.rows : unsupportedMCP || expired ? [] : sample?.rows ?? []
  const interval = mcp ? Math.max(15, Math.min(86400, sample?.expected_interval_seconds ?? 60)) * 1000 : 0
  const providerDelay = mcp && sample?.next_attempt_at && sample.observed_at ? Math.max(0, Date.parse(sample.next_attempt_at) - Date.parse(sample.observed_at) - interval) : 0
  const staleAfter = mcp ? 2 * interval + providerDelay : card.data.kind === 'dataset' && card.data.name === 'blerg.engine.usage' ? 11 * 60_000 : 30_000
  const stale = !!sample?.observed_at && now - Date.parse(sample.observed_at) > staleAfter
  const status = card.data.kind === 'inline' ? null
    : unsupportedMCP ? 'unsupported-live · Use native stat or gauge without sparklines'
    : !context?.files.subscribeDataset ? 'Live data unavailable'
    : !sample ? 'Connecting'
    : expired ? 'Data expired'
    : sample.status === 'access_removed' ? sample.cause === 'sample_expired' ? 'Data expired' : 'Access removed'
    : sample.status === 'disconnected' ? 'Disconnected'
    : sample.status === 'unavailable' ? unavailableLabel(sample.cause)
    : card.data.kind === 'dataset' && card.data.name === 'blerg.cards.merged' ? 'Observed'
    : stale ? 'Delayed' : 'Live'
  return {ref, rows, status, observed: sample?.observed_at, nextAttempt: sample?.status === 'unavailable' && Number.isFinite(Date.parse(sample.next_attempt_at ?? '')) ? sample?.next_attempt_at : undefined}
}

function unavailableLabel(cause?: string): string {
  const labels: Record<string, string> = {
    provider_rate_limited: 'Rate limited', mapping_failed: 'Invalid sample', invalid_result: 'Invalid sample',
    gateway_unavailable: 'Gateway unavailable', authority_unavailable: 'Authority unavailable',
    execution_unavailable: 'Gateway unavailable', attempt_store_unavailable: 'Audit unavailable', attempt_commit_failed: 'Audit unavailable',
    audit_unavailable: 'Audit unavailable', clock_skew: 'Clock skew', connection_needs_sign_in: 'Connection needs sign-in',
  }
  return cause && Object.hasOwn(labels, cause) ? labels[cause] : 'Source unavailable'
}
