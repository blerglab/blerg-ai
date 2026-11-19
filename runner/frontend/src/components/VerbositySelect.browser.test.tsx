import { render, screen, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { expect, it, vi } from 'vitest'
import { browserWidths } from '../test/browserWidths'
import { VerbositySettings, SessionVerbosity } from './VerbositySelect'
import '../index.css'
import './SettingsDialog.css'
vi.mock('../apiFetch', () => ({apiFetch:vi.fn(async()=>new Response('{"verbosity":"standard"}'))}))
it.each(browserWidths([390,3840]))('keeps verbosity usable at %i px',async width=>{
 await page.viewport(width,844)
 render(<main style={{maxWidth:680,padding:16}}><VerbositySettings /><SessionVerbosity id="session-1" value="standard" /></main>)
 await userEvent.click(screen.getByText('Session verbosity'))
 const field=screen.getByLabelText('Default verbosity')
 await waitFor(()=>expect(field).toBeEnabled())
 await userEvent.selectOptions(field,'terse')
 await expect.element(field).toHaveValue('terse')
 const session=screen.getByLabelText('Verbosity')
 await userEvent.selectOptions(session,'narrated')
 await expect.element(session).toHaveValue('narrated')
 expect(session.getBoundingClientRect().right).toBeLessThanOrEqual(width)
 expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})
