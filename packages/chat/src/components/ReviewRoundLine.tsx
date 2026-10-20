// The one line a round of review answers draws in the transcript (model/reviewRounds.ts):
// "✓ 11 requests answered · report.md", which opens the review. Until the round's review file is
// read, the count is the round's publishes (one per `review reply` call); then it is the requests
// the round settled, against the review before it.
import { createContext, useContext, useEffect, useState } from 'react'
import { useChat } from './context'
import { parseReviewFile, type ReviewFile } from '../model/review'
import { answeredInRound, reviewedFileName, type ReviewRound, type ReviewRounds } from '../model/reviewRounds'
import type { AgentEvent, ArtifactPayload } from '../types'
import { TimeLabel } from './TimeLabel'

/** The transcript's review rounds, so a line can find its own by its event. */
export const ReviewRoundsContext = createContext<ReviewRounds | null>(null)

function plural(n: number): string {
  return `${n} request${n === 1 ? '' : 's'} answered`
}

export function ReviewRoundLine({ ev }: { ev: AgentEvent }) {
  const key = ev.client_event_id
  const p = ev.payload as ArtifactPayload
  const known = useContext(ReviewRoundsContext)?.rounds.get(key)
  const round: ReviewRound = known ?? { file: reviewedFileName(p.name) ?? p.name, publishes: 1, latest: p, first: p.version === 1 }
  const chat = useChat()
  const files = chat?.files ?? null
  const sessionId = chat?.sessionId ?? ''
  const latestId = round.latest.id
  const baselineId = round.baseline?.id
  const [read, setRead] = useState<{ count: number; review: ReviewFile } | null>(null)

  useEffect(() => {
    // Without the review before the round (a page not loaded) the publishes are the best count.
    if (!files || (!baselineId && !round.first)) return
    let live = true
    const load = async (id: string) => parseReviewFile(await (await files.raw(sessionId, id)).text())
    void (async () => {
      try {
        const [latest, baseline] = await Promise.all([load(latestId), baselineId ? load(baselineId) : Promise.resolve(null)])
        if (live && latest) setRead({ count: answeredInRound(latest, baseline), review: latest })
      } catch {
        // The publishes stay the count.
      }
    })()
    return () => { live = false }
  }, [files, sessionId, latestId, baselineId, round.first])

  const count = read?.count ?? round.publishes
  const open = chat?.openReview
  const text = <>✓ {plural(count)} · {round.file}</>
  return (
    <div className="agent-card marker review-round" data-testid="review-round" data-event-id={key}>
      {open
        ? (
            <button type="button" className="review-round-open" title={`Open the review of ${round.file}`}
              onClick={() => open(round.file, read?.review.file.artifactId)}>
              {text}
            </button>
          )
        : <span>{text}</span>}
      <TimeLabel ts={ev.ts} className="card-time-end" />
    </div>
  )
}
