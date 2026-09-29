// Setup for the real-browser (Playwright) test project. Unlike the jsdom setup,
// this avoids jsdom-only shims and jest-dom (which isn't browser-compatible).
// Its only job: unmount React trees after each test so mounted terminals don't
// accumulate across tests sharing one browser page.
import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'

afterEach(() => {
  cleanup()
  // Remove any stray host nodes appended directly to <body> by tests.
  document.querySelectorAll('body > div').forEach(n => n.remove())
})
