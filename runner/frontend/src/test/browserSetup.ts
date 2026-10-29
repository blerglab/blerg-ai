// Setup for the real-browser (Playwright) test project. Unlike the jsdom setup,
// this avoids jsdom-only shims and jest-dom (which isn't browser-compatible).
// Its only job: unmount React trees after each test so mounted terminals don't
// accumulate across tests sharing one browser page.
import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'
import { clearViews } from '../lib/sessionViewCache'
import { clearViewCache } from '../../../../packages/chat/src/model/viewCache'

afterEach(() => {
  cleanup()
  // Preferences survive remounts within a case, but never leak into another case's fixtures.
  // A retained History pane can otherwise request data an unrelated fixture does not serve.
  localStorage.clear()
  // Nor a session's cached explorer and panel.
  clearViews()
  // Nor the chat's kept Files lists and file contents (#281).
  clearViewCache()
  // Remove any stray host nodes appended directly to <body> by tests.
  document.querySelectorAll('body > div').forEach(n => n.remove())
})
