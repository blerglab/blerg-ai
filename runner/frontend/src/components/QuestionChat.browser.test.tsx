import { render, screen, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { AskReply, QuestionCard, QuestionPanel, QuestionThreadsProvider, type QuestionThreadPage, type QuestionThreadsClient } from '@blerglab/chat'
import { setTheme } from '../../../../packages/settings/theme'
import '../index.css'
import '@blerglab/chat/blerg.css'

const shots = import.meta.env.VITE_QUESTION_SHOT_DIR
const plan = 'Plan changes · 2 add\n\nMilestone: 0.6.0 · Revision 3\n\n- Add: BLG-541 · Keep decisions bound to the reviewed revision\n  Reason: Your decision\n  Thrust: Reliable decisions\n- Add: BLG-552 · Questions read as a small chat\n  Reason: Your decision\n  Thrust: Reliable decisions\n\nExpires in 24 hours.'
for (const width of [390, 3840]) for (const mode of ['dialog', 'tab'] as const) for (const state of ['your-turn', 'main-replying', 'long-question', 'plan-decision', 'colorblind'] as const) {
 it(`shows ${mode} ${state} at ${width}px`, async () => {
  await page.viewport(width, 1000)
  setTheme(state === 'colorblind' ? 'colorblind-dark' : 'dark')
  const body = state === 'plan-decision' ? plan : state === 'long-question' ? 'How should we roll out the new question conversation?\n\n' + 'Consider existing clients, a safe migration, rollback, privacy and the people waiting for an answer. '.repeat(45) : 'Deploy this release to staging?\n\nThe staging environment is ready. Shall I deploy the reviewed revision?'
  const data: QuestionThreadPage = { question: { id: 'q', session_id: 'main', body, status: 'open', answerable: true, decision: state === 'plan-decision', question_revision: 3, question_turn: state === 'main-replying' ? 'main' : 'person' }, entries: state === 'main-replying' ? [{ seq: 1, author: 'person', kind: 'reply', body: 'Will production stay on the current release?', created_at: '2026-11-05T12:00:00Z' }] : [], more: false }
  const client: QuestionThreadsClient = { load: vi.fn(async () => data), answer: vi.fn(async () => true), dismiss: vi.fn(async () => true), reply: vi.fn(async () => {}), openTab: () => true }
  render(<QuestionThreadsProvider client={client}>{mode === 'tab' ? <div style={{height: '100dvh'}}><QuestionPanel id="q" title={body.split('\n')[0]} onClose={() => {}} /></div> : <QuestionCard id="q" question={body} audience="you"><AskReply binary={state === 'plan-decision'} messageId="q" answer={async () => true} /></QuestionCard>}</QuestionThreadsProvider>)
  if (mode === 'dialog') await userEvent.click(screen.getByRole('button', { name: /Deploy this|How should|Plan changes/ }))
  const surface = mode === 'dialog' ? screen.getByRole('dialog') : screen.getByRole('region')
  if (state === 'plan-decision') expect(await within(surface).findByRole('button', {name:'Approve'})).toBeVisible()
  else expect(await within(surface).findByRole('textbox', {name:'Your answer'})).toBeVisible()
  if (state === 'main-replying') expect(await within(surface).findByText('Main is replying…')).toBeVisible()
  else expect(within(surface).queryByText('Your turn')).toBeNull()
  expect(surface.scrollWidth).toBeLessThanOrEqual(surface.clientWidth)
  const header = surface.querySelector('header')!
  const title = header.querySelector<HTMLElement>('.ticket-modal-title')!
  expect(title.title).toBe(body.split('\n')[0])
  const titleText = title.querySelector('.md-inline') ?? title
  expect(getComputedStyle(titleText).whiteSpace).toBe('normal')
  const conversation = surface.querySelector<HTMLElement>('.question-conversation')!
  expect(conversation).not.toHaveTextContent(body.split('\n')[0])
  if (state === 'long-question' && (mode === 'dialog' || width === 390)) expect(conversation.scrollHeight).toBeGreaterThan(conversation.clientHeight)
  if (mode === 'dialog' && state === 'your-turn') expect(surface.getBoundingClientRect().height).toBeLessThan(500)
  if (mode === 'tab') expect(surface.getBoundingClientRect().height).toBe(1000)
  expect(within(surface).getByRole('button',{name:'Dismiss'}).closest('header')).toBe(header)
  for (const button of surface.querySelectorAll('button')) expect(button.title).not.toBe('')
  if (shots) await page.screenshot({path: `${shots}/${mode}-${state}-${width}.png`})
 })
}

for (const width of [390, 3840]) for (const mode of ['dialog', 'tab'] as const) for (const worker of ['Main', 'BLG-514 · Improve question layout']) {
 it(`names asker ${worker} in ${mode} at ${width}px`, async () => {
  await page.viewport(width, 1000)
  setTheme('dark')
  const body = 'Approve the question layout?\n\nThe revised layout is ready for review.'
  const data: QuestionThreadPage = { question: { id: 'asker', session_id: worker === 'Main' ? 'main' : 'worker', body, status: 'open', answerable: true, decision: true }, entries: [], more: false }
  const client: QuestionThreadsClient = { load: async () => data, answer: async () => true, dismiss: async () => true, reply: vi.fn(async () => {}) }
  render(<QuestionThreadsProvider client={client}>{mode === 'tab' ? <div style={{height: '100dvh'}}><QuestionPanel id="asker" title={body} worker={worker} /></div> : <QuestionCard id="asker" question={body} worker={worker} audience="you"><AskReply binary messageId="asker" answer={async () => true} /></QuestionCard>}</QuestionThreadsProvider>)
  if (mode === 'dialog') await userEvent.click(screen.getByRole('button', { name: /Approve the question layout/ }))
  const surface = mode === 'dialog' ? screen.getByRole('dialog') : screen.getByRole('region')
  const label = worker === 'Main' ? 'Ask Main' : 'Ask BLG-514'
  const ask = await within(surface).findByRole('button', { name: label })
  expect(ask.title).toContain(worker)
  expect(surface.scrollWidth).toBeLessThanOrEqual(surface.clientWidth)
  if (shots) await page.screenshot({path: `${shots}/asker-${worker === 'Main' ? 'main' : 'worker'}-${mode}-${width}.png`})
  await userEvent.click(ask)
  expect(within(surface).getByRole('button', { name: label }).title).toContain(worker)
 })
}

const paragraphQuestion = 'Would you approve the updated question layout after checking migration notes, existing clients, rollback plan, and the full question text on a narrow screen? Please review all details before deciding.'
for (const width of [390, 3840]) for (const mode of ['dialog', 'tab'] as const) for (const state of ['short', 'paragraph', 'decision'] as const) {
 it(`wraps the complete ${state} title once in ${mode} at ${width}px`, async () => {
  await page.viewport(width, 1000)
  setTheme('dark')
  const title = state === 'paragraph' ? paragraphQuestion : state === 'decision' ? 'Approve the reviewed question layout and keep every decision bound to its exact revision?' : 'Approve the question layout?'
  if (state === 'paragraph') expect(title).toHaveLength(200)
  const body = state === 'paragraph' ? title : `${title}\n\nThe revised layout is ready for review.`
  const data: QuestionThreadPage = { question: { id: 'wrap', session_id: 'worker', body, status: 'open', answerable: true, decision: state === 'decision' }, entries: [], more: false }
  const client: QuestionThreadsClient = { load: async () => data, answer: async () => true, dismiss: async () => true, reply: async () => {}, openTab: () => true }
  render(<QuestionThreadsProvider client={client}>{mode === 'tab' ? <div style={{ height: '100dvh' }}><QuestionPanel id="wrap" title={title} worker="BLG-514 · Improve question layout" onClose={() => {}} /></div> : <QuestionCard id="wrap" question={body} worker="BLG-514 · Improve question layout" audience="you"><AskReply binary={state === 'decision'} messageId="wrap" answer={async () => true} /></QuestionCard>}</QuestionThreadsProvider>)
  if (mode === 'dialog') await userEvent.click(screen.getByRole('button', { name: new RegExp(title.slice(0, 15)) }))
  const surface = mode === 'dialog' ? screen.getByRole('dialog') : screen.getByRole('region')
  await within(surface).findByRole('button', { name: 'Dismiss' })
  const heading = within(surface).getByRole('heading', { name: title })
  expect(heading).toHaveTextContent(title)
  expect(getComputedStyle(heading).whiteSpace).toBe('normal')
  expect(getComputedStyle(heading).textOverflow).not.toBe('ellipsis')
  expect(heading.scrollWidth).toBeLessThanOrEqual(heading.clientWidth)
  expect(heading.scrollHeight).toBeLessThanOrEqual(heading.clientHeight + 1)
  expect(surface.querySelector('.question-conversation')).not.toHaveTextContent(title)
  if (width === 390 && state === 'paragraph') expect(heading.getBoundingClientRect().height).toBeGreaterThan(parseFloat(getComputedStyle(heading).lineHeight) * 2)
  const header = surface.querySelector('header')!
  for (const button of header.querySelectorAll('button')) expect(button.getBoundingClientRect().top).toBeLessThanOrEqual(header.getBoundingClientRect().top + 2)
  expect(surface.scrollWidth).toBeLessThanOrEqual(surface.clientWidth)
  if (shots) await page.screenshot({path: `${shots}/wrap-${state}-${mode}-${width}.png`})
 })
}
