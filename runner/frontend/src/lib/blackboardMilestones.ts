type MilestoneState = { frozen_at?: string | null; stage?: string }

/** The current milestone follows record order; archived milestones remain reachable. */
export function newestOpenMilestone<T extends MilestoneState>(milestones: readonly T[]): T | undefined {
 return milestones.findLast(m => !m.frozen_at && m.stage !== 'Finalizing' && m.stage !== 'Final')
}
