import { contentExportScreenshot } from './src/test/contentExportScreenshot'
import { layoutScreenshot } from './src/test/layoutScreenshot'
import { needsYouScreenshot } from './src/test/needsYouScreenshot'
import {testPerformanceScreenshot} from './src/test/testPerformanceScreenshot'
import { settingsScreenshot } from './src/test/settingsScreenshot'
import { questionScreenshot } from './src/test/questionScreenshot'
import {chartFrames, startChartCapture, stopChartCapture, chartScreenshot} from './src/test/chartFrames'
import {startDatasetFixture, stopDatasetFixture} from './src/test/datasetServer'
import { transcriptInput, workspaceKey, workspaceTrace, workspaceTaskTime } from './src/test/transcriptInput'
import { defineConfig, defineProject } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { playwright } from '@vitest/browser-playwright'
import type { Plugin } from 'vite'
import { chatAliases } from './vite.config'
import { startBlackboardFixture, stopBlackboardFixture } from './src/test/blackboardServer'
import { startSideChannelFixture, stopSideChannelFixture } from './src/test/sideChannelServer'
import { whatsNewBackend, startWhatsNewFixture, stopWhatsNewFixture } from './src/test/whatsNewServer'
import { openLockHolderPage, releaseLockHolderPage } from './src/test/silentRefreshLockCommands'

// A request recorder for the browser tests that prove a diagram makes no request: anything under
// /__beacon/ is recorded (and answered 404), and /__beacon_hits lists what was recorded.
function beacon(): Plugin {
  const hits: string[] = []
  return {
    name: 'test-beacon',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (req.url?.startsWith('/__beacon/')) {
          hits.push(req.url)
          res.statusCode = 404
          res.end()
        } else if (req.url === '/__beacon_hits') {
          res.setHeader('content-type', 'application/json')
          res.setHeader('cache-control', 'no-store')
          res.end(JSON.stringify(hits))
        } else next()
      })
    },
  }
}

// Three test projects:
//   • unit    — fast, jsdom, fake timers. Pure logic + DOM-structure tests.
//   • browser — real browsers (Playwright). The ONLY layer that can test
//               scrolling, because scroll depends on real layout
//               (scrollHeight / clientHeight / scrollTop) which jsdom does not
//               implement. Scroll/touch/device-switch regressions kept slipping
//               through precisely because they were never under a real browser.
//   • browser-performance — strict timing budgets, serial files after all other projects.
// Browser tests are named *.browser.test.tsx and excluded from the unit project.
const prebundled = [
  'react',
  'react-dom',
  'react-dom/client',
  'react/jsx-dev-runtime',
  'react/jsx-runtime',
  '@testing-library/react',
  '@xterm/xterm',
  '@testing-library/user-event',
  'react-router-dom',
  'zustand',
  'zustand/react/shallow',
  '@xterm/addon-fit',
  'd3-array',
  'd3-time-format',
  '@tiptap/core',
  '@tiptap/extension-table',
  '@tiptap/extension-task-item',
  '@tiptap/extension-task-list',
  '@tiptap/markdown',
  '@tiptap/pm/state',
  '@tiptap/pm/view',
  '@tiptap/react',
  '@tiptap/starter-kit',
  'dompurify',
  'react-markdown',
  'remark-gfm',
  'pdfjs-dist',
  'mermaid',
  'vega',
  'vega-lite',
  'vega-interpreter',
  'highlight.js/lib/languages/dockerfile',
  'highlight.js/lib/languages/makefile',
]

// Chromium loses its startup hover/fine-pointer capabilities after CDP touch emulation
// is disabled; neither clearing media overrides nor detaching CDP restores them. Vitest
// isolates iframes, but reuses the outer page across files. Keep these device-mutating
// fixtures on their own browser instance so desktop hover tests never inherit that page.
const touchEmulationFiles = [
  'src/components/ChannelRecommendation.browser.test.tsx',
  'src/components/BlackboardRecommendation.browser.test.tsx',
]

function browserProject(performance: boolean) {
  return defineProject({
    plugins: [react(), beacon(), whatsNewBackend()],
    cacheDir: performance ? '../../.cache/vite-runner-performance' : '../../.cache/vite-runner-browser',
    resolve: { alias: chatAliases },
    // An inline project does not inherit the root's optimizeDeps: without its own list, a cold
    // cache found react-dom/client partway through the run and reloaded the page.
    optimizeDeps: { include: prebundled },
    test: {
      name: performance ? 'browser-performance' : 'browser',
      // Bound simultaneous pages so the corpus and interactive tests share one browser
      // without starving pointer/animation work on machines with many reported CPUs.
      maxWorkers: performance ? 1 : 4,
      // A strict <16 ms sample must not compete with other test files or projects.
      fileParallelism: !performance,
      sequence: { groupOrder: performance ? 2 : 1 },
      // Only this app's files: the Board's browser tests have their own project in board/web,
      // because files outside this root are not pre-scanned and a cold cache reloaded mid-run.
      include: performance ? ['src/**/*.performance.browser.test.{ts,tsx}'] : ['src/**/*.browser.test.{ts,tsx}'],
      exclude: performance ? [] : ['src/**/*.performance.browser.test.{ts,tsx}'],
      // cleanup() after each test so mounted terminals don't accumulate across
      // tests (they share one browser page and would otherwise leak).
      setupFiles: ['./src/test/browserSetup.ts'],
      // No fake timers: scrolling needs real requestAnimationFrame + real layout.
      browser: {
        enabled: true,
        commands: {
          questionScreenshot,
          settingsScreenshot,
          needsYouScreenshot,
          testPerformanceScreenshot,
          chartFrames,
          chartScreenshot,
          startChartCapture,
          stopChartCapture,
          transcriptInput,
          workspaceKey,
          workspaceTrace,
          workspaceTaskTime,
          startDatasetFixture,
          stopDatasetFixture,
          startBlackboardFixture,
          stopBlackboardFixture,
          startSideChannelFixture,
          stopSideChannelFixture,
          layoutScreenshot,
          contentExportScreenshot,
          startWhatsNewFixture,
          stopWhatsNewFixture,
          openLockHolderPage,
          releaseLockHolderPage,
          async setUsageReducedMotion({ page }, reduce: boolean) {
            await page.emulateMedia({ reducedMotion: reduce ? 'reduce' : 'no-preference' })
          },
        },
        // The test iframe scales to fit Playwright's outer viewport. Owner
        // captures need enough room to retain the requested pixel dimensions.
        // Every browser runs in UTC, the timezone CI's runners use, so date rendering (Mermaid Gantt
        // axes, formatted times) is the same on every machine.
        // Native scrollbar drags are part of the transcript input contract.
        provider: playwright((process.env.VITE_COMPOSER_SHOTS_DIR || process.env.VITE_INBOX_SHOTS_DIR || process.env.VITE_QUESTION_SHOT_DIR || process.env.VITE_BOUND_ASK_SHOTS_DIR || process.env.VITE_KEEPER_SHOTS_DIR || process.env.VITE_CARDTIME_SHOTS_DIR || process.env.VITE_BLACKBOARD_SHOTS_DIR || process.env.VITE_INSIGHTS_SHOTS_DIR || process.env.VITE_IMAGE_ZOOM_SHOTS_DIR || process.env.VITE_LOADER_SHOTS_DIR || process.env.VITE_LOADER_REDUCED_MOTION || process.env.VITE_TAB_FOCUS_SHOTS_DIR || process.env.VITE_FILE_TAB_SHOTS_DIR || process.env.VITE_LAUNCH_SHOTS_DIR || process.env.VITE_CHROME_SHOTS_DIR || process.env.VITE_SHELL_TAB_SHOTS_DIR || process.env.VITE_POPOUT_SHOTS_DIR || process.env.VITE_SEND_ACK_SHOTS_DIR || process.env.VITE_VISUAL_DOWNLOAD_SHOTS_DIR || process.env.VITE_VISUAL_REFLOW_SHOTS_DIR || process.env.VITE_WORKER_NOTICE_SHOTS_DIR || process.env.VITE_THEME_SHOTS_DIR || process.env.VITE_VISUAL_FLOWCHART_SHOTS_DIR || process.env.VITE_VISUAL_SEQUENCE_SHOTS_DIR || process.env.VITE_VISUAL_GANTT_SHOTS_DIR || process.env.VITE_LIVE_FILE_SHOTS_DIR) ? {
          launchOptions: { ignoreDefaultArgs: ['--hide-scrollbars'] },
          contextOptions: { timezoneId: 'UTC', viewport: { width: 3840, height: process.env.VITE_COMPOSER_SHOTS_DIR ? 4000 : 2160 }, ...(process.env.VITE_LOADER_REDUCED_MOTION ? { reducedMotion: 'reduce' as const } : {}) },
        } : { launchOptions: { ignoreDefaultArgs: ['--hide-scrollbars'] }, contextOptions: { timezoneId: 'UTC' } }),
        headless: true,
        instances: [
          { browser: 'chromium', exclude: performance ? [] : ['src/**/*.performance.browser.test.{ts,tsx}', ...touchEmulationFiles] },
          ...(!performance ? [{ browser: 'chromium' as const, name: 'browser (chromium touch)', include: touchEmulationFiles, fileParallelism: false }] : []),
          // Shortcut ownership and withdrawal labels must also hold in Firefox.
          // Playwright's Firefox focus emulation does not cover Vitest's child iframe:
          // input in another page can leave activeElement set but suppress native Enter
          // activation. Keep Firefox files serial; Chromium still exercises four workers.
          // https://github.com/microsoft/playwright/issues/42933
          ...(!performance && process.env.VITE_BLERG_TEST_PHASE !== 'touched' ? [{ browser: 'firefox' as const, fileParallelism: false, include: ['src/components/ChartWorker.browser.test.tsx', 'src/components/ChannelCategories.browser.test.tsx', 'src/components/WorkersDock.browser.test.tsx', 'src/components/WhatsNew.browser.test.tsx', 'src/components/HostShellConsentWithdrawal.browser.test.tsx', 'src/components/HostShellConsentDialog.browser.test.tsx', 'src/components/ShellView.browser.test.tsx', 'src/components/ShellEnded.browser.test.tsx', 'src/components/ChildQuestions.browser.test.tsx', 'src/components/MessageThread.withdrawn.browser.test.tsx', 'src/components/KeyboardShortcuts.browser.test.tsx', 'src/components/PaletteClaim.browser.test.tsx', 'src/components/CommandPalette.browser.test.tsx', 'src/components/PaletteOwnership.browser.test.tsx', 'src/components/ShellWorkspace.browser.test.tsx', 'src/components/SessionTabOverflow.browser.test.tsx', 'src/components/WorkspaceSessionLabels.browser.test.tsx', 'src/components/SelectAllScoping.browser.test.tsx'] }] : []),
        ],
      },
    },
  })
}

export default defineConfig({
  // Keep test caches in this checkout when node_modules is a shared symlink.
  cacheDir: '../../.cache/vite-runner',
  resolve: { alias: chatAliases },
  // Pre-bundle these so the browser project doesn't trigger a mid-run Vite
  // re-optimize (which aborts the first test with "Failed to fetch dynamically
  // imported module").
  optimizeDeps: { include: prebundled },
  test: {
    projects: [
      {
        cacheDir: '../../.cache/vite-runner-unit',
        plugins: [react()],
        resolve: { alias: chatAliases },
        test: {
          name: 'unit',
          environment: 'jsdom',
          testTimeout: 30000,
          hookTimeout: 30000,
          setupFiles: ['./src/test/setup.ts'],
          include: ['src/**/*.test.{ts,tsx}'],
          exclude: ['src/**/*.browser.test.{ts,tsx}', 'node_modules/**'],
          fakeTimers: {
            toFake: ['requestAnimationFrame', 'cancelAnimationFrame', 'setTimeout', 'clearTimeout'],
          },
        },
      },
      browserProject(false),
      browserProject(true),
    ],
  },
})
