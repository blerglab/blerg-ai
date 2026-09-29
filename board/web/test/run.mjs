// Behaviour test for useStickToBottom — `npm run test:scroll` from web/.
//
// Auto-follow is a browser-layout property (scrollTop vs scrollHeight across
// real reflows), so it is checked in a real engine rather than asserted about
// a mock: this boots the vite dev server on test/harness.html, drives headless
// Chromium over the DevTools Protocol, and prints one line per check. No test
// runner and no new npm dependencies — Node's built-in WebSocket plus whatever
// chromium is on PATH (or $CHROMIUM). Exits non-zero on the first failure set.
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { createConnection } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";

const PORT = 5199, CDP = 9223;
const chromium = process.env.CHROMIUM ??
  ["chromium", "chromium-browser", "google-chrome", "chrome"]
    .find((c) => spawnSync("which", [c]).status === 0);
if (!chromium) {
  console.error("no chromium on PATH — install one or set $CHROMIUM");
  process.exit(1);
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Refuse to start if either fixed port is already listening. Once we are
// talking to a server we cannot tell ours from a squatter, and a stale vite
// from another checkout serves ITS tree — which scores this checkout's code
// against someone else's: green for code that fails, red for code that passes.
// (Both were observed; this script used to leak the dev server that caused it.)
// Probe each port on the same host the suite will later talk to: vite binds
// whatever `localhost` resolves to (::1 here, so a 127.0.0.1-only probe misses
// a squatter that fetch() would happily use), chromium is pinned to 127.0.0.1.
const inUse = (port, host) => new Promise((res) => {
  const s = createConnection({ port, host })
    .on("connect", () => { s.destroy(); res(true); })
    .on("error", () => res(false));
  s.setTimeout(1000, () => { s.destroy(); res(false); });
});
for (const [port, host, what] of [[PORT, "localhost", "dev server"], [CDP, "127.0.0.1", "chromium debug port"]]) {
  if (await inUse(port, host)) {
    console.error(`port ${port} (${what}) is already in use — free it and re-run. ` +
      `Testing against a server this script did not start would test the wrong tree.`);
    process.exit(1);
  }
}

const profile = mkdtempSync(join(tmpdir(), "stick-"));
const kids = [];
let tearingDown = false;

// detached: every child leads its own process group, so the negative-pid kill
// below takes its whole tree. `npx vite` runs the real dev server as a
// grandchild, and killing just the npx wrapper left that grandchild holding
// PORT — which the readiness probe below would then happily accept as "vite is
// up", serving whatever tree the orphan was started from.
const start = (cmd, args) => {
  const k = spawn(cmd, args, { stdio: "ignore", detached: true });
  kids.push(k);
  return k;
};
const isReaped = (k) => k.exitCode !== null || k.signalCode !== null;
// skip reaped children: their pid is free for the OS to hand to someone else,
// and -pid on a recycled group would kill a stranger
const killGroup = (k, signal) => {
  if (isReaped(k)) return;
  try { process.kill(-k.pid, signal); } catch { /* group already gone */ }
};
const reaped = (k) => (isReaped(k) ? Promise.resolve() : new Promise((r) => k.once("exit", r)));

const cleanup = async () => {
  tearingDown = true;
  // SIGTERM before SIGKILL so both get to exit cleanly — chromium in
  // particular finishes writing its profile back, which is what makes the
  // removal below deterministic rather than a race. (It still leaves a few
  // unreaped renderers behind on a PID 1 that does not reap orphans; they are
  // zombies, already dead, and a normal init clears them at once.)
  for (const k of kids) killGroup(k, "SIGTERM");
  await Promise.race([Promise.all(kids.map(reaped)), sleep(3000)]);
  for (const k of kids) killGroup(k, "SIGKILL"); // whatever ignored SIGTERM
  // only remove the profile once chromium is really gone: signals are
  // asynchronous, and a profile still being written back deletes as ENOTEMPTY
  // — which `force` does NOT suppress (it only swallows ENOENT)
  await Promise.race([Promise.all(kids.map(reaped)), sleep(1000)]);
  for (let i = 0; ; i++) {
    try { return rmSync(profile, { recursive: true, force: true }); }
    catch (e) { if (i === 4) throw e; await sleep(100); }
  }
};
const finish = async (code) => { await cleanup(); process.exit(code); };
// last resort for paths that never reach finish() (an unhandled rejection, a
// signal): synchronous, so it can kill but not wait
process.on("exit", () => { for (const k of kids) killGroup(k, "SIGKILL"); });
for (const sig of ["SIGINT", "SIGTERM"]) process.on(sig, () => finish(130));

const waitFor = async (probe, what) => {
  for (let i = 0; i < 150; i++) {
    try { const v = await probe(); if (v) return v; } catch { /* not up yet */ }
    await sleep(100);
  }
  throw new Error(`timed out waiting for ${what}`);
};

const vite = start("npx", ["vite", "--port", String(PORT), "--strictPort"]);
start(chromium, ["--headless", "--disable-gpu", "--no-sandbox", "--hide-scrollbars",
  `--remote-debugging-port=${CDP}`, "--remote-debugging-address=127.0.0.1",
  `--user-data-dir=${profile}`, "about:blank"]);

// --strictPort means our vite exits rather than sliding to another port when
// PORT is taken. Treat that as fatal instead of testing on: the probe below
// cannot tell our dev server from a squatter, so carrying on would score this
// checkout's code against someone else's tree — green for code that fails, red
// for code that passes, depending on whose tree is squatting.
vite.once("exit", (code) => {
  if (tearingDown) return;
  console.error(`vite exited (code ${code}) before the suite started — port ${PORT} is ` +
    `probably already in use. Free it and re-run; testing against a server this ` +
    `script did not start would test the wrong working tree.`);
  finish(1);
});

await waitFor(() => fetch(`http://localhost:${PORT}/test/harness.html`).then((r) => r.ok), "vite");
const target = await waitFor(async () => {
  const list = await (await fetch(`http://127.0.0.1:${CDP}/json/list`)).json();
  return list.find((t) => t.type === "page");
}, "chromium");

const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((r) => ws.addEventListener("open", r, { once: true }));
let msgId = 0;
const waiters = new Map();
ws.addEventListener("message", (m) => {
  const msg = JSON.parse(m.data);
  waiters.get(msg.id)?.(msg);
  waiters.delete(msg.id);
});
const cdp = (method, params = {}) => new Promise((res, rej) => {
  const id = ++msgId;
  waiters.set(id, (m) => (m.error ? rej(new Error(JSON.stringify(m.error))) : res(m.result)));
  ws.send(JSON.stringify({ id, method, params }));
});
const evalJS = async (expression) => {
  const r = await cdp("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description ?? "eval failed");
  return r.result.value;
};

await cdp("Page.enable");
await cdp("Page.navigate", { url: `http://localhost:${PORT}/test/harness.html` });
await waitFor(() => evalJS("!!window.harness"), "harness");

// settle: two frames plus a tick — long enough for React to commit, for the
// composer's rAF resize, and for the scroll events our own pins provoke
const settle = "new Promise(r => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(r, 40))))";
const act = (js) => evalJS(`(async () => { ${js}; await ${settle}; return null; })()`);
const state = (js = "") => evalJS(`(async () => { ${js}; await ${settle}; return window.harness.state(); })()`);
const scrollTo = (px) => act(`document.getElementById('convo').scrollTop = ${px}`);
const scrollAboveBottom = (px) =>
  act(`const e = document.getElementById('convo'); e.scrollTop = e.scrollHeight - e.clientHeight - ${px}`);

let failed = 0;
const check = (name, ok, detail) => {
  if (!ok) failed++;
  console.log(`${ok ? "ok  " : "FAIL"}  ${name} — ${detail}`);
};
const atBottom = (s) => s.dist <= 2;

let s = await state();
check("mounts at the latest message", atBottom(s), JSON.stringify(s));

s = await state("window.harness.append()");
check("follows new events while at the bottom", atBottom(s), JSON.stringify(s));

await scrollTo(0);
s = await state("window.harness.append()");
check("a new event does not move a scrolled-up reader", s.top === 0, JSON.stringify(s));

s = await state("window.harness.append(5)");
check("a burst of events does not move a scrolled-up reader", s.top === 0, JSON.stringify(s));

await scrollAboveBottom(120);
const before = await state();
s = await state("window.harness.append()");
check("120px above the bottom does not resume follow", s.top === before.top, `top ${before.top} -> ${s.top}`);

await scrollAboveBottom(20);
s = await state("window.harness.append()");
check("20px above the bottom resumes follow", atBottom(s), JSON.stringify(s));

// send() must land exactly at the bottom even though the composer grows a line
// in a rAF after the handler runs, shrinking the conversation underneath it
await scrollTo(0);
s = await state("window.harness.send()");
check("send() lands at the bottom despite the composer growing", atBottom(s), JSON.stringify(s));
s = await state("window.harness.append()");
check("send() re-arms follow", atBottom(s), JSON.stringify(s));

// remount (minimize + restore): the reader's position died with the old node,
// so both cases come back at the latest message and following again
await act("window.harness.mounted(false)");
s = await state("window.harness.mounted(true)");
check("remount while following returns to the latest", atBottom(s), JSON.stringify(s));

await scrollTo(0);
await act("window.harness.mounted(false)");
s = await state("window.harness.mounted(true)");
check("remount while scrolled up returns to the latest", atBottom(s), JSON.stringify(s));
s = await state("window.harness.append()");
check("remount re-arms follow", atBottom(s), JSON.stringify(s));

// a different session is a different transcript: scrolling up in the old one
// says nothing about the new one ("Run again" keeps the same DOM node)
await scrollTo(0);
s = await state("window.harness.switchSession()");
check("a new session starts at its own latest message", atBottom(s), JSON.stringify(s));
s = await state("window.harness.append()");
check("a new session follows its own events", atBottom(s), JSON.stringify(s));

ws.close();
console.log(failed ? `\n${failed} check(s) failed` : "\nall checks passed");
await finish(failed ? 1 : 0);
