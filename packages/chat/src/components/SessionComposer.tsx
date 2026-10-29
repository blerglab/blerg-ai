// Draft state belongs to the input: typing must not render transcripts or visited viewers.
import { useState } from 'react'
import Composer, { type ComposerProps } from './Composer'
import { getDraft, setDraft as saveDraft, type ChatStorage } from '../model/storage'

export default function SessionComposer({ sessionId, storage, onSubmit, ...props }: Omit<ComposerProps, 'draft' | 'onDraft' | 'onSubmit'> & {
  sessionId: string
  storage: ChatStorage
  onSubmit: (text: string) => Promise<boolean>
}) {
  const [draft, setDraft] = useState(() => getDraft(storage, sessionId))
  function change(text: string) {
    setDraft(text)
    saveDraft(storage, sessionId, text)
  }
  return <Composer {...props} draft={draft} onDraft={change} onSubmit={() => {
    void onSubmit(draft.trim()).then(sent => { if (sent) change('') })
  }} />
}
