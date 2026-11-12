// BLG-473 theme audit: the real Runner app (not a stripped page), each theme, 390 and 3840 px.
// Screenshots only — not a CI assertion — written to AUDIT_SHOTS for someone to look at.
// Follows the pattern of core/web/tests/outside-session-audit.mjs: a real Vite dev server, a real
// browser, the actual app entry. The session list and transcript are driven entirely by
// /ws/browser (see ws.ts), not REST, so the "server" here is a minimal page.routeWebSocket() mock
// that answers initial_state and subscribe_session instead of a live daemon connection.
import { createServer } from 'vite';
import { chromium } from 'playwright';

const THEMES = ['dark', 'light', 'colorblind-dark', 'colorblind-light', 'high-contrast',
  'catppuccin-mocha', 'catppuccin-latte', 'dracula', 'nord', 'tokyo-night'];
const OUT = process.env.AUDIT_SHOTS;
if (!OUT) { console.log('Set AUDIT_SHOTS=<dir> to write screenshots. Skipping.'); process.exit(0); }

const INITIAL_STATE = {
  type: 'initial_state',
  server_release: '0.0.0-audit',
  daemons: [
    { id: 'd1', name: 'workstation', mode: 'local', version: '1.0.0', repos_root: '/home/dev/repos', status: 'connected', last_seen_at: new Date().toISOString() },
  ],
  sessions: [
    { id: 's1', daemon_id: 'd1', status: 'running', project_path: '/home/dev/repos/widget', repo: 'widget', title: 'Build the widget', started_at: new Date().toISOString(), unread: false },
    { id: 's2', daemon_id: 'd1', status: 'waiting', project_path: '/home/dev/repos/widget', repo: 'widget', title: 'Review the diff', started_at: new Date().toISOString(), unread: true },
    { id: 's3', daemon_id: 'd1', status: 'idle', project_path: '/home/dev/repos/gizmo', repo: 'gizmo', title: 'Fix the flaky test', started_at: new Date().toISOString(), starred: true },
    { id: 's4', daemon_id: 'd1', status: 'ended', project_path: '/home/dev/repos/gizmo', repo: 'gizmo', title: 'Ship the release', started_at: new Date().toISOString(), ended_at: new Date().toISOString() },
  ],
};

// A chat transcript with a code block, a Mermaid diagram and a Vega-Lite chart — the three real
// surfaces the review asked for, not just an empty/home screen.
const ASSISTANT_TEXT = `Here is the build pipeline, a quick chart of last week's runs, and the retry helper.

\`\`\`mermaid
flowchart LR
  A[Build] --> B[Test]
  B --> C[Deploy]
  B -->|flaky| A
\`\`\`

\`\`\`vega-lite
{"$schema":"https://vega.github.io/schema/vega-lite/v6.json","data":{"values":[{"day":"Mon","runs":12},{"day":"Tue","runs":9},{"day":"Wed","runs":14},{"day":"Thu","runs":7},{"day":"Fri","runs":11}]},"mark":"bar","encoding":{"x":{"field":"day","type":"nominal","sort":null},"y":{"field":"runs","type":"quantitative"}}}
\`\`\`

\`\`\`typescript
export function withRetry<T>(fn: () => Promise<T>, attempts = 3): Promise<T> {
  return fn().catch(err => attempts > 1 ? withRetry(fn, attempts - 1) : Promise.reject(err))
}
\`\`\`
`;

function agentEvent(sessionId, seq, kind, payload) {
  return { type: 'agent_event', session_id: sessionId, client_event_id: `ev-${seq}`, seq, ts: new Date().toISOString(), kind, payload };
}

function routeSession(page) {
  return page.routeWebSocket('**/ws/browser', ws => {
    ws.send(JSON.stringify(INITIAL_STATE));
    ws.onMessage(raw => {
      let msg; try { msg = JSON.parse(String(raw)); } catch { return; }
      if (msg.type !== 'subscribe_agent_events') return;
      const id = msg.session_id;
      ws.send(JSON.stringify(agentEvent(id, 1, 'user_message', { text: 'Can you show the build pipeline, a chart of last week\'s runs, and the retry helper?', source: 'person' })));
      ws.send(JSON.stringify(agentEvent(id, 2, 'assistant_text', { text: ASSISTANT_TEXT, done: true })));
      ws.send(JSON.stringify({ type: 'agent_events_replay_done', session_id: id, last_seq: 2, has_more: false }));
    });
  });
}

async function waitForWorkspace(page, timeout) {
  await page.locator('img.visual-diagram, [data-testid="visual-failed"]').first().waitFor({ timeout });
  await page.locator('[aria-label="Vega-Lite preview"] canvas, [aria-label="Vega-Lite preview"] svg, [data-testid="visual-failed"]').first().waitFor({ timeout });
  await page.getByText('withRetry').first().waitFor({ timeout });
}

const server = await createServer({ server: { host: '127.0.0.1', port: 0 } });
let browser;
try {
  await server.listen();
  browser = await chromium.launch({ headless: true });
  const base = server.resolvedUrls.local[0];

  // Vite's dependency optimizer only discovers vega/vega-lite/mermaid the first time a page
  // actually imports them, at which point it bundles and force-reloads the page — which would
  // otherwise race the very first workspace screenshot below. Prime it once, outside the loop.
  {
    const primer = await browser.newPage({ viewport: { width: 1280, height: 1000 } });
    await routeSession(primer);
    await primer.route('**/api/**', route => route.fulfill({ json: {} }));
    await primer.route('**/auth/provider', route => route.fulfill({ json: { id: 'local' } }));
    await primer.goto(`${base}sessions/s1#access_token=fixture`);
    await waitForWorkspace(primer, 60_000);
    await primer.close();
  }

  let shots = 0;
  for (const width of [390, 3840]) {
    for (const theme of THEMES) {
      const page = await browser.newPage({ viewport: { width, height: 1000 } });
      await page.addInitScript(theme => localStorage.setItem('blerg-theme', theme), theme);
      await routeSession(page);
      await page.route('**/api/**', route => route.fulfill({ json: {} }));
      await page.route('**/auth/provider', route => route.fulfill({ json: { id: 'local' } }));
      await page.goto(`${base}#access_token=fixture`);
      await page.getByText('Build the widget').waitFor();
      await page.screenshot({ path: `${OUT}/runner-sessions-${width}-${theme}.png` });
      shots++;

      // An open workspace: real chat with a code block, a Mermaid diagram and a Vega-Lite chart.
      await page.goto(`${base}sessions/s1#access_token=fixture`);
      await waitForWorkspace(page, 30_000);
      await page.waitForTimeout(300); // let the diagram/chart finish their layout pass before the shot
      await page.screenshot({ path: `${OUT}/runner-workspace-${width}-${theme}.png` });
      shots++;

      // Insights, Time tab — fetch the project's own rich fixture through the real dev server so
      // the page renders real-looking figures, not an empty/error state.
      const insights = await page.evaluate(async () => {
        const m = await import('/src/test/insightsFixture.ts');
        const a = await import('/src/test/activityFixture.ts');
        return { data: m.richInsights, prices: m.richPrices, activity: a.richActivity };
      });
      await page.route('**/api/insights*', route => route.fulfill({ json: insights.data }));
      await page.route('**/api/insights/prices', route => route.fulfill({ json: insights.prices }));
      await page.route('**/api/insights/activity*', route => route.fulfill({ json: insights.activity }));
      await page.goto(`${base}insights/time#access_token=fixture`);
      await page.getByText('Updated', { exact: false }).first().waitFor({ timeout: 15_000, state: 'attached' });
      await page.screenshot({ path: `${OUT}/runner-insights-time-${width}-${theme}.png` });
      shots++;

      // Settings, opened the way a person does: the sidebar/tab-bar entry, not a direct route.
      await page.goto(`${base}sessions#access_token=fixture`);
      await page.getByText('Build the widget').waitFor();
      // The phone tab bar (settings-tab) and the desktop sidebar (settings-nav) each render only
      // one of the two at a given width.
      await page.getByTestId('settings-tab').or(page.getByTestId('settings-nav')).first().click();
      await page.getByRole('heading', { name: 'Settings' }).waitFor();
      await page.screenshot({ path: `${OUT}/runner-settings-${width}-${theme}.png` });
      shots++;

      await page.close();
    }
  }
  console.log(`Wrote ${shots} Runner screenshots to ${OUT}`);
} finally {
  await browser?.close();
  await server.close();
}
