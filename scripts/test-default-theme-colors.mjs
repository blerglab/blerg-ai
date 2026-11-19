// Focused resolved-CSS regression, independent of layout/performance suites.
// Fixture captured from main ab133ab3; the light Board diff correction is intentional.
import { chromium } from 'playwright';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
process.chdir(fileURLToPath(new URL('..', import.meta.url)));
const groups = {
  runner: {
    files: ['runner/frontend/src/index.css', 'runner/frontend/src/components/PermissionsModal.css', 'runner/frontend/src/components/SessionInsights.css', 'runner/frontend/src/components/ActivityInsights.css'],
    html: '<button class="btn primary">Save</button><button class="pm-primary">Save</button><div class="session-insights-dialog"><span class="si-seg si-s-1">1</span><span class="si-seg si-s-2">2</span><span class="si-seg si-s-3">3</span></div>' + ['tests','build_lint','git','read','edit','other','model','waiting_on_person','idle'].map(k => `<i class="activity-${k}"></i>`).join(''),
    probes: {'.btn.primary':['color'], '.pm-primary':['color'], '.si-s-1':['color','backgroundColor'], '.si-s-2':['color','backgroundColor'], '.si-s-3':['color','backgroundColor'], ...Object.fromEntries(['tests','build_lint','git','read','edit','other','model','waiting_on_person','idle'].map(k=>['.activity-'+k,['backgroundColor']]))},
  },
  board: {files:['board/web/src/styles.css'], html:'<button class="btn">Save</button><div class="diff"><div class="dl add">+</div><div class="dl del">−</div></div>', probes:{'.btn':['color'],'.dl.add':['color'],'.dl.del':['color']}},
  settings: {files:['packages/settings/settings.css'],html:'<input class="settings-toggle-control" type="checkbox">',probes:{'.settings-toggle-control::before':['backgroundColor']}},
  chat: {files:['packages/chat/src/theme/blerg.css','packages/chat/src/components/chat.css','packages/chat/src/components/diff.css','packages/chat/src/components/markup/markup.css'],html:'<button style="color:var(--chat-accent-fg)">Accent</button><button class="diff-comment-add">+</button><span class="markup-pin-badge">1</span><button class="markup-send">Send</button>',probes:{button:['color'],'.diff-comment-add':['color'],'.markup-pin-badge':['color'],'.markup-send':['color']}},
};
const fixture = JSON.parse(readFileSync(new URL('../core/internal/landing/default_component_colors_test.json', import.meta.url)));
const browser = await chromium.launch({headless:true});
let checks = 0;
try {
  const page = await browser.newPage();
  const tokens = readFileSync('brand/tokens.css','utf8');
  for (const [group, spec] of Object.entries(groups)) {
    const css = tokens + spec.files.map(path=>readFileSync(path,'utf8').replace(/@import[^;]+;/g,'')).join('\n');
    for (const theme of ['dark','light']) {
      // Explicit defaults and unpinned OS defaults must both resolve identically.
      for (const pinned of [true,false]) {
        await page.emulateMedia({colorScheme:theme});
        await page.setContent(`<html ${pinned ? `data-theme="${theme}"` : ''}><head><style>${css}</style></head><body>${spec.html}</body></html>`);
        const actual = await page.evaluate(probes=>Object.fromEntries(Object.entries(probes).map(([selector,props])=>{
          const [target,pseudo] = selector.split('::');
          const style = getComputedStyle(document.querySelector(target),pseudo ? '::'+pseudo : null);
          return [selector,Object.fromEntries(props.map(prop=>[prop,style[prop]]))];
        })),spec.probes);
        assert.deepEqual(actual,fixture[group][theme],`${group} ${theme} pinned=${pinned}`);
        checks += Object.values(actual).reduce((n,p)=>n+Object.keys(p).length,0);
      }
    }
  }
  console.log(`PASS ${checks} resolved default component colors (explicit and OS defaults)`);
} finally {await browser.close();}
