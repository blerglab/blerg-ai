import { screen } from '@testing-library/react'

/** Read layout first so CSS animations have been created, then wait for this rail's entrance.
 * Looking at document.getAnimations() before that read can miss an unpainted drawer entirely. */
export async function railReady(): Promise<void> {
  const rail = screen.getByTestId('session-rail')
  rail.getBoundingClientRect()
  await Promise.all(rail.getAnimations().map(animation => animation.finished))
}
