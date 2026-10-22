import { useEffect } from 'react'
import { useTicker } from '@blerglab/chat'
import { useToastStore } from './useToastStore'
import { proposalExpiring, warnedProposalDeadlines as warned } from '../lib/proposalAttention'
import type { ProposalInbox } from './useProposalInbox'
import type { ProposalInfo } from '../types'

// notifyExpiring tells the person that proposals enter their last 24 hours. It is in-app only, by
// the owner's ruling (0.4.4): no push notification, with or without contents. Sessions are
// semi-ephemeral, and a proposal open long enough to need one means something is wrong.
function notifyExpiring(due: ProposalInfo[]) {
  useToastStore.getState().addToast({ title: due.length === 1 ? 'Proposal expires soon' : 'Proposals expire soon', body: `${due.length} pending ${due.length === 1 ? 'request expires' : 'requests expire'} within 24 hours. Review or extend before the deadline.`, url: `/proposals?id=${encodeURIComponent(due[0].id)}` })
}

// The warning that proposals enter their last 24 hours, once per deadline (however many bells or
// inboxes call it: `warned` is shared). App's Layout calls it, so it comes on every page.
export function useProposalExpiryWarnings(inbox: ProposalInbox) {
  const now = useTicker(true)
  const serverNow = now - (inbox.clockOffset ?? 0)
  useEffect(() => {
    const newlyDue = inbox.pending.filter(p => proposalExpiring(p, serverNow) && !warned.has(`${p.id}:${p.expires_at}`))
    for (const p of newlyDue) warned.add(`${p.id}:${p.expires_at}`)
    if (newlyDue.length) notifyExpiring(newlyDue)
  }, [inbox.pending, serverNow])
}

