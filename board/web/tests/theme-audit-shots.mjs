// BLG-473 theme audit: the real Board app (not a stripped page), each theme, 390 and 3840 px.
// Screenshots only — not a CI assertion — written to AUDIT_SHOTS for someone to look at.
// Follows the pattern of core/web/tests/outside-session-audit.mjs: a real Vite dev server, a real
// browser, the actual app entry, with only the network boundary (fetch) mocked.
import { createServer } from 'vite';
import { chromium } from 'playwright';

const THEMES = ['dark', 'light', 'colorblind-dark', 'colorblind-light', 'high-contrast'];
const OUT = process.env.AUDIT_SHOTS;
if (!OUT) { console.log('Set AUDIT_SHOTS=<dir> to write screenshots. Skipping.'); process.exit(0); }

const BOARD_ID = '11111111-1111-1111-1111-111111111111';
const BOARD = {
  id: BOARD_ID, key: 'BLG', name: 'Blerg', description: 'The Blerg project board.',
  deploy_url: null, git_base: 'main',
  model: '', reviewer_model: '', discuss_model: '', chat_model: '',
  field_schema: [], require_repo: false, gate_enabled: true, gate_on_unavailable: 'wait', gate_on_dispute: 'hold',
  driven_by: null, concurrency: 1, ci_policy: 'required',
  automation_engine: 'claude', automation_token_set: true, automation_token_expires_at: null,
  automation_account_id: null, repos: ['blerglab/blerg'],
};
const COLUMNS = [
  { id: 'c1', board_id: BOARD_ID, rank: '1', name: 'Ready', is_terminal: false },
  { id: 'c2', board_id: BOARD_ID, rank: '2', name: 'In progress', is_terminal: false },
  { id: 'c3', board_id: BOARD_ID, rank: '3', name: 'Done', is_terminal: true },
];
const makeCard = (n, column_id, title, type, priority, size) => ({
  id: `card-${n}`, board_id: BOARD_ID, number: n, rank: String(n), column_id, type, title,
  body: 'A short description of the work.', priority, size, risk: 'medium', complexity: 'low',
  model: '', fields: {}, dedup_key: null, external_id: null, archived_at: null, version: 1,
  gate_flag: null, auto_merge: true, merged_sha: null, merged_at: null, run_attempts: 0,
  stuck_at: null, stale_at: null, created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z',
  repos: ['blerglab/blerg'], tags: ['frontend'], links: null, depends_on: null, blockers: null,
});
const CARDS = [
  makeCard(101, 'c1', 'Add dark-mode icon set', 'feature', 'P2', 'M'),
  makeCard(102, 'c1', 'Investigate flaky upload test', 'bug', 'P1', 'S'),
  makeCard(103, 'c2', 'Theme audit: every surface follows the central theme', 'feature', 'P1', 'L'),
  makeCard(104, 'c3', 'Board tab filters in a popover', 'feature', 'P2', 'M'),
];

const server = await createServer({ server: { host: '127.0.0.1', port: 0 } });
let browser;
try {
  await server.listen();
  browser = await chromium.launch({ headless: true });
  for (const width of [390, 3840]) {
    for (const theme of THEMES) {
      const page = await browser.newPage({ viewport: { width, height: 1000 } });
      await page.addInitScript(theme => localStorage.setItem('blerg-theme', theme), theme);
      // Playwright gives priority to the most recently registered matching route, so the
      // catch-all goes first and the specific fixtures after, to override it.
      await page.route('**/api/**', route => route.fulfill({ json: {} }));
      await page.route('**/api/me', route => route.fulfill({ json: { kind: 'service', is_admin: true, is_human: true, provider_subject: 'alex' } }));
      await page.route('**/api/boards', route => route.fulfill({ json: [
        { id: BOARD_ID, key: 'BLG', name: 'Blerg', template: 'plain' },
      ] }));
      await page.route('**/api/overview', route => route.fulfill({ json: [
        { board_id: BOARD_ID, ready: 2, in_progress: 1, to_test: 0,
          token_week: [1, 2, 3, 2, 1, 0, 2], activity_week: [1, 2, 3, 2, 1, 0, 2] },
      ] }));
      await page.route('**/api/sessions', route => route.fulfill({ json: [] }));
      await page.route(`**/api/boards/${BOARD_ID}`, route => route.fulfill({ json: BOARD }));
      await page.route(`**/api/boards/${BOARD_ID}/columns`, route => route.fulfill({ json: COLUMNS }));
      await page.route(`**/api/boards/${BOARD_ID}/cards/view**`, route => route.fulfill({ json: { cards: CARDS, older: {} } }));
      await page.route(`**/api/boards/${BOARD_ID}/active-sessions`, route => route.fulfill({ json: {} }));
      await page.route(`**/api/boards/${BOARD_ID}/deployments`, route => route.fulfill({ json: [] }));
      await page.route(`**/api/boards/${BOARD_ID}/run`, route => route.fulfill({ json: { running: false } }));
      await page.route(`**/api/boards/${BOARD_ID}/releases`, route => route.fulfill({ json: [] }));
      await page.route('**/auth/provider', route => route.fulfill({ json: { id: 'local' } }));
      await page.goto(`${server.resolvedUrls.local[0]}#access_token=fixture`);
      await page.locator('.boards').waitFor();
      await page.screenshot({ path: `${OUT}/board-home-${width}-${theme}.png` });

      await page.goto(`${server.resolvedUrls.local[0]}boards/${BOARD_ID}#access_token=fixture`);
      await page.getByText(CARDS[0].title).waitFor({ state: 'attached' });
      await page.screenshot({ path: `${OUT}/board-columns-${width}-${theme}.png` });
      await page.close();
    }
  }
  console.log(`Wrote ${THEMES.length * 2 * 2} Board screenshots to ${OUT}`);
} finally {
  await browser?.close();
  await server.close();
}
