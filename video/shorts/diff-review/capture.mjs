// Capture for the diff-review short: the person asks for a change, the agent publishes a diff,
// the person comments on two lines and submits, the agent fixes both and posts the diff again,
// and the new version shows the two threads answered.
//
//   node video/shorts/diff-review/capture.mjs desktop|phone
// (video/capture/run.sh resets the demo before each take and runs both.)
//
// Marks (seconds from the take's start), used by spec.yaml's scenes: open, typed, sent,
// diff-posted, viewer, comment-1, comment-2, submitted, fixed, viewer-2, new-code, end.
import { take } from '../../capture/lib.mjs'

const device = process.argv[2] || 'desktop'
const PROMPT = 'Uploads from the app fail on flaky networks. Retry them with exponential backoff, add tests, and show me the diff.'
const COMMENTS = [
  ['delay = base_delay * 2 ** attempt', 'Cap this: with retries=8 the last wait is over a minute.'],
  ['sleep(delay)', "Add jitter, or every phone retries at the same moment after an outage."],
]

await take('diff-review', device, async (t) => {
  const p = t.page
  const phone = device === 'phone'
  await p.goto(t.runnerUrl(`/sessions/${t.seed.sessions.uploads}`))
  const box = p.locator('textarea[placeholder^="Message the agent"]')
  await box.waitFor()
  if (!phone) {
    // Keep the session panel open on the right (it is the default; a stored choice may differ).
    const rail = p.getByTestId('rail-toggle')
    if (await rail.count() && (await rail.getAttribute('aria-expanded')) === 'false') await rail.click()
  }
  await t.pause(1.2)
  t.mark('open')

  // 1. Ask for the change.
  await t.type(box, PROMPT, phone ? 28 : 34)
  t.mark('typed')
  await t.pause(0.5)
  await p.getByRole('button', { name: 'Send' }).click()
  t.mark('sent')

  // 2. The agent works and publishes a diff (scripted engine: about ten seconds).
  await t.wait('agent works', async () => {
    await p.getByText('comment on any line').waitFor({ timeout: 90000 })
  })
  await t.pause(1.5)
  t.mark('diff-posted')

  // 3. Open it and review two lines.
  const card = p.getByTestId('artifact-preview-diff').last()
  await card.scrollIntoViewIfNeeded()
  await t.pause(0.8)
  await card.click()
  const viewer = p.getByTestId('artifact-viewer-overlay')
  await viewer.waitFor()
  await t.pause(1.5)
  t.mark('viewer')

  for (const [i, [line, text]] of COMMENTS.entries()) {
    const row = viewer.locator('tr.diff-row.diff-add', { hasText: line }).first()
    await row.scrollIntoViewIfNeeded()
    await row.evaluate((el) => el.scrollIntoView({ block: 'center', behavior: 'smooth' }))
    await t.pause(0.9)
    await row.locator('button.diff-line-select').click()
    await t.pause(0.5)
    await viewer.getByRole('button', { name: 'Request change' }).click()
    const form = viewer.locator('form[aria-label="Inline comment"]').last()
    await t.type(form.locator('textarea'), text, phone ? 26 : 32)
    await t.pause(0.5)
    await form.getByRole('button', { name: 'Save' }).click()
    await t.pause(1.2)
    t.mark(`comment-${i + 1}`)
  }

  await viewer.getByRole('button', { name: 'Submit' }).click()
  await t.pause(1.2)
  // Submitting may close the viewer itself; otherwise close it to watch the agent in the chat.
  if (await viewer.isVisible()) await viewer.getByRole('button', { name: 'Close', exact: true }).click()
  await viewer.waitFor({ state: 'hidden' })
  await t.pause(0.6)
  t.mark('submitted')

  // 4. The agent fixes both, answers both and posts the diff again.
  await t.wait('agent fixes', async () => {
    await p.getByText('your two comments answered').waitFor({ timeout: 90000 })
  })
  await t.pause(1.5)
  await p.getByText('your two comments answered').scrollIntoViewIfNeeded()
  await t.pause(1.5)
  t.mark('fixed')

  // 5. The new version: the threads came along, answered.
  const card2 = p.getByTestId('artifact-preview-diff').last()
  await card2.scrollIntoViewIfNeeded()
  await t.pause(0.6)
  await card2.click()
  await viewer.waitFor()
  await t.pause(1.2)
  // The card opens the version that was reviewed; the new one is a click away.
  const latest = viewer.getByRole('button', { name: 'View latest' }).or(viewer.getByText('View latest'))
  if (await latest.count()) {
    await latest.first().click()
    await t.pause(1.2)
  }
  t.mark('viewer-2')
  const fixedLine = viewer.locator('tr.diff-row.diff-add', { hasText: 'min(max_delay' }).first()
  if (await fixedLine.count()) {
    await fixedLine.evaluate((el) => el.scrollIntoView({ block: 'center', behavior: 'smooth' }))
    await t.pause(2.5)
  }
  t.mark('new-code')
  const thread = viewer.getByText('Capped at max_delay').first()
  if (await thread.count()) {
    await thread.evaluate((el) => el.scrollIntoView({ block: 'center', behavior: 'smooth' }))
  }
  await t.pause(4)
  await t.shot('final')
  t.mark('end')
})
