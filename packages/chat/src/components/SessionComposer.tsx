// Draft state belongs to the input: typing must not render transcripts or visited viewers.
import { useEffect, useRef, useState } from 'react'
import Composer, { type ComposerProps } from './Composer'
import { getDraft, setDraft as saveDraft, type ChatStorage } from '../model/storage'

export default function SessionComposer({ sessionId, storage, onSubmit, recoveryDrafts, claimRecoveryInput, ...props }: Omit<ComposerProps, 'draft' | 'onDraft' | 'onSubmit'> & {
  sessionId: string
  storage: ChatStorage
  recoveryDrafts?: readonly string[]
  claimRecoveryInput?: (sessionId: string, clientId: string) => Promise<string | null>
  onSubmit: (text: string) => Promise<boolean>
}) {
  const [draft, setDraft] = useState(() => getDraft(storage, sessionId))
  const requested = useRef(new Set<string>())
  const legacyReturned = useRef(new Set<string>())
  useEffect(() => {
    // Retire the per-browser markers from chat 1.60.3; the server owns return
    // receipts now. Clearing an old marker never grants a second return.
    const prefix = `blerg.recovered-input.${sessionId}.`
    for (const key of storage.removePrefix?.(prefix) ?? []) legacyReturned.current.add(key.slice(prefix.length))
  }, [sessionId, storage])
  useEffect(() => {
    for (const id of recoveryDrafts ?? []) {
      if (requested.current.has(id)) continue
      const key = `blerg.recovered-input.${sessionId}.${id}`
      const wasReturned = legacyReturned.current.has(id) || storage.get(key) === '1'
      storage.set(key, null)
      if (!claimRecoveryInput) continue
      requested.current.add(id)
      void claimRecoveryInput(sessionId, id).then(text => {
        if (text === null || wasReturned) return
        const restored = [getDraft(storage, sessionId), text].filter(Boolean).join('\n\n')
        // Persist even when navigation unmounted this input during the claim.
        saveDraft(storage, sessionId, restored)
        setDraft(restored)
      }).catch(() => { requested.current.delete(id) })
    }
  }, [recoveryDrafts, claimRecoveryInput, sessionId, storage])
  function change(text: string) {
    setDraft(text)
    saveDraft(storage, sessionId, text)
  }
  return <Composer {...props} draft={draft} onDraft={change} onSubmit={() => {
    void onSubmit(draft.trim()).then(sent => { if (sent) change('') })
  }} />
}
