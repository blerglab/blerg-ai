import { expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import SkillInsights from './SkillInsights'
import { richActivity, emptyActivity } from '../test/activityFixture'

it('says plainly when skill evidence is unavailable, without a chart or cards', () => {
 render(<SkillInsights activity={{ ...richActivity, skills: { ...richActivity.skills!, unavailable: true } }} />)
 expect(screen.getByText(/Skill evidence is temporarily unavailable/)).toBeInTheDocument()
 expect(screen.queryByTestId(/^skill-card-/)).toBeNull()
})

it('says plainly when no skill loads were reported', () => {
 render(<SkillInsights activity={emptyActivity} />)
 expect(screen.getByText(/No skill loads have been reported in this range/)).toBeInTheDocument()
})

// BLG-464: a card per skill leads with its verdict — one glyph plus a short visible word, not a
// hollow icon alone — then three numbers, scannable without reading a sentence; loads move into
// the meta line; the evidence paragraphs move behind "Evidence by version".
it('renders each loaded skill as a glanceable card: a verdict word beside the name, then a stat row', () => {
 render(<SkillInsights activity={richActivity} />)
 const ux = screen.getByTestId('skill-card-ux-design')
 expect(within(ux).getByRole('heading', { level: 2 })).toHaveTextContent('UX design')
 // Not enough comparable reviews across the two recorded versions: an unknown-state verdict, not a
 // sentence that claims the skill is or isn't helping.
 const uxVerdict = within(ux).getByRole('img', { name: /Helping: Not enough comparable reviews across versions yet\./ })
 expect(uxVerdict).toHaveTextContent('Not enough evidence yet')
 expect(ux).toHaveTextContent('2 loads')
 expect(ux).toHaveTextContent('2 conversations')
 expect(ux).toHaveTextContent('2 versions')
 const uxStats = within(ux).getByTestId('skill-stats-ux-design')
 expect(within(uxStats).getAllByRole('definition').map(d => d.textContent)).toEqual(['0/1', '50%', '1'])

 const tdd = screen.getByTestId('skill-card-test-driven-development')
 expect(within(tdd).getByRole('heading', { level: 2 })).toHaveTextContent('Testing')
 const tddVerdict = within(tdd).getByRole('img', { name: /Helping: Not enough version evidence yet\./ })
 expect(tddVerdict).toHaveTextContent('Not enough evidence yet')
 expect(tdd).toHaveTextContent('1 load')
 expect(tdd).toHaveTextContent('1 conversation')
 const tddStats = within(tdd).getByTestId('skill-stats-test-driven-development')
 expect(within(tddStats).getAllByRole('definition').map(d => d.textContent)).toEqual(['—', '—', '0'])
})

it('explains the measurement basis behind a disclosure, not as a standalone paragraph', () => {
 render(<SkillInsights activity={richActivity} />)
 const details = screen.getByText('How this is measured').closest('details')!
 expect(details).not.toHaveAttribute('open')
 expect(within(details).getByText(/A load shows that instructions entered context/)).toBeInTheDocument()
 // Not duplicated outside the disclosure.
 expect(screen.getAllByText(/A load shows that instructions entered context/)).toHaveLength(1)
})

it('keeps the full evidence reachable behind one disclosure per skill, not removed', () => {
 render(<SkillInsights activity={richActivity} />)
 const ux = screen.getByTestId('skill-card-ux-design')
 const details = within(ux).getByText('Evidence by version').closest('details')!
 expect(details).not.toHaveAttribute('open')
 expect(within(details).getAllByRole('heading', { level: 3 }).map(h => h.textContent)).toEqual(['Version 1', 'Version 2'])
 // The rule-level evidence (previously its own run-on sentence) is still present, just inside the
 // disclosure.
 expect(within(details).getByText(/Plain words|Screenshots inspected/)).toBeInTheDocument()
})
