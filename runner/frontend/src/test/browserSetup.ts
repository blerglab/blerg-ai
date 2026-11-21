import { invalidateBlackboardViews } from '../lib/blackboardViewCache'
import { collectBrowserErrors } from './browserErrors'
import { clearInsightsCache } from '../lib/insightsCache'
// Setup for the real-browser (Playwright) test project. Unlike the jsdom setup,
// this avoids jsdom-only shims and jest-dom (which isn't browser-compatible).
// Unmount React trees and report background errors against individual cases.
import { beforeEach } from 'vitest'
import { cleanup } from '@testing-library/react'
import { clearViews } from '../lib/sessionViewCache'
import { clearViewCache } from '../../../../packages/chat/src/model/viewCache'

const browserErrors = collectBrowserErrors(window, (message, cause) => console.error(message, cause))

beforeEach(({ onTestFinished }) => {
  onTestFinished(async () => {
    try {
      cleanup()
      clearInsightsCache()
      invalidateBlackboardViews()
      // Preferences survive remounts within a case, but never leak into another case's fixtures.
      // A retained History pane can otherwise request data an unrelated fixture does not serve.
      localStorage.clear()
      // Nor a session's cached explorer and panel.
      clearViews()
      // Nor the chat's kept Files lists and file contents (#281).
      clearViewCache()
      // Remove any stray host nodes appended directly to <body> by tests.
      document.querySelectorAll('body > div').forEach(n => n.remove())
    } finally {
      // A task boundary lets the browser dispatch rejections from the final microtask
      // checkpoint, including work started by unmount and the file's teardown hooks.
      await new Promise<void>(resolve => {
        const channel = new MessageChannel()
        channel.port1.onmessage = () => { channel.port1.close(); channel.port2.close(); resolve() }
        channel.port2.postMessage(null)
      })
      browserErrors.assertEmpty()
    }
  })
})
