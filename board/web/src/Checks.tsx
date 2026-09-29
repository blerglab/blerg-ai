import { useEffect, useState } from "react";
import { api, CardEvent } from "./api";
import { RunnerSession } from "./Conversation";

// Checks: everything a reviewer wants stated natively on the card — tests,
// coverage, lint, version bumps, migrations — extracted from what the card
// already knows (session events, then comments). Sessions are asked to close
// with an explicit "Checks:" block (see buildCardPrompt); explicit lines win,
// heuristics cover lazy or historical sessions.

export interface TestStatus { passed: boolean; detail: string; counted?: boolean }
export interface CoverageStatus { pct: number; before?: number; approx?: boolean }
export interface LintStatus { passed: boolean; detail: string } // "clean (go vet)" | "3 issues"
export interface Checks {
  tests: TestStatus | null;
  coverage: CoverageStatus | null;
  lint: LintStatus | null;
  version: string | null;        // "1.4.0" — newly set version, if any
  migrations: string[];          // migration files touched, e.g. ["007_deploy_url.sql"]
}

// unquoteJSON undoes double-encoded event payloads from older runner builds.
function unquoteJSON(s: string): string {
  if (s.startsWith('"')) {
    try { const v = JSON.parse(s); if (typeof v === "string") return v; } catch { /* raw */ }
  }
  return s;
}

export function extractTestStatus(text: string): TestStatus | null {
  // vitest / jest: "Tests  3 failed | 31 passed (34)" or "Tests  34 passed (34)"
  let m = text.match(/Tests\s+(?:(\d+)\s+failed\s*\|\s*)?(\d+)\s+passed\s*(?:\((\d+)\))?/);
  if (m) {
    const failed = Number(m[1] ?? 0), passed = Number(m[2]);
    const total = Number(m[3] ?? passed + failed);
    return { passed: failed === 0, detail: `${passed}/${total} tests`, counted: true };
  }
  // "100 of 100 tests pass" / "97/100 tests passing"
  m = text.match(/(\d+)\s*(?:of|\/)\s*(\d+)\s+tests?\s+pass/i);
  if (m) return { passed: m[1] === m[2], detail: `${m[1]}/${m[2]} tests`, counted: true };
  // raw `go test ./...` output
  const goOk = text.match(/^ok\s+\S+/gm)?.length ?? 0;
  const goFail = /^(?:--- )?FAIL/m.test(text);
  if (goFail) return { passed: false, detail: "tests failing", counted: true };
  if (goOk > 0) return { passed: true, detail: `${goOk} package${goOk === 1 ? "" : "s"} ok`, counted: true };
  // prose
  if (/\b(?:make test|all tests|the tests|tests)\s+(?:are\s+)?pass(?:es|ed|ing)?\b/i.test(text)) {
    return { passed: true, detail: "tests passing" };
  }
  if (/\b(?:make test|tests)\s+(?:is\s+|are\s+)?fail(?:s|ed|ing)?\b/i.test(text)) {
    return { passed: false, detail: "tests failing" };
  }
  return null;
}

export function extractCoverage(text: string): CoverageStatus | null {
  // explicit before/after: "coverage: 68.4% -> 71.2%" / "71.2% (was 68.4%)"
  let m = text.match(/coverage:?\s*(\d+(?:\.\d+)?)%\s*(?:->|→|to)\s*(\d+(?:\.\d+)?)%/i);
  if (m) return { pct: Number(m[2]), before: Number(m[1]) };
  m = text.match(/coverage:?\s*(\d+(?:\.\d+)?)%\s*\(\s*was\s+(\d+(?:\.\d+)?)%\s*\)/i);
  if (m) return { pct: Number(m[1]), before: Number(m[2]) };
  // `go tool cover -func` total
  m = text.match(/total:\s*\(statements\)\s*(\d+(?:\.\d+)?)%/);
  if (m) return { pct: Number(m[1]) };
  // istanbul / vitest coverage table
  m = text.match(/(?:All files[^|]*\|\s*|Statements\s*:\s*)(\d+(?:\.\d+)?)/);
  if (m) return { pct: Number(m[1]) };
  // bare per-package `go test -cover` lines — average, flagged approximate
  const per = [...text.matchAll(/coverage:\s*(\d+(?:\.\d+)?)%\s+of\s+statements/g)];
  if (per.length === 1) return { pct: Number(per[0][1]) };
  if (per.length > 1) {
    const avg = per.reduce((a, x) => a + Number(x[1]), 0) / per.length;
    return { pct: Math.round(avg * 10) / 10, approx: true };
  }
  return null;
}

export function extractLint(text: string): LintStatus | null {
  // explicit Checks line or prose: "lint: clean", "go vet is clean", "gofmt applied"
  let m = text.match(/\b(?:lint|go vet|gofmt|staticcheck|golangci-lint|static analysis)\b[^.\n]{0,40}\b(?:is\s+)?(clean|passes|pass(?:ed|ing)?|applied|no issues)\b/i);
  if (m) return { passed: true, detail: "clean" };
  // eslint: "✖ 3 problems (2 errors, 1 warning)"
  m = text.match(/[✖x]\s*(\d+)\s+problems?\s*\((\d+)\s+errors?/);
  if (m) return { passed: Number(m[2]) === 0, detail: `${m[1]} problem${m[1] === "1" ? "" : "s"}` };
  // golangci-lint: "3 issues:" / "0 issues."
  m = text.match(/(\d+)\s+issues?[.:]/);
  if (m) return { passed: m[1] === "0", detail: m[1] === "0" ? "clean" : `${m[1]} issues` };
  m = text.match(/\b(?:lint|vet)\b[^.\n]{0,30}\bfail(?:s|ed|ing)?\b/i);
  if (m) return { passed: false, detail: "failing" };
  return null;
}

export function extractVersion(text: string): string | null {
  // explicit: "version: 1.4.0" (Checks block) or prose "bumped version to 1.4.0"
  let m = text.match(/^\s*-?\s*version:?\s+v?(\d+\.\d+(?:\.\d+)?)/im);
  if (m) return m[1];
  m = text.match(/\bbump(?:ed)?\s+(?:the\s+)?version\s+(?:number\s+)?to\s+v?(\d+\.\d+(?:\.\d+)?)/i);
  if (m) return m[1];
  m = text.match(/\bversion\s+is\s+now\s+v?(\d+\.\d+(?:\.\d+)?)/i);
  if (m) return m[1];
  return null;
}

export function extractMigrations(text: string): string[] {
  // migration files by convention: migrations/NNN_name.sql (paths in tool
  // calls, filenames in prose or Checks lines)
  const out = new Set<string>();
  for (const m of text.matchAll(/migrations?\/(\d{3,}[\w.-]*\.sql)/g)) out.add(m[1]);
  for (const m of text.matchAll(/\b(\d{3,}_[\w.-]*\.sql)\b/g)) out.add(m[1]);
  return [...out];
}

interface RunnerEvent { seq: number; kind: string; payload: Record<string, unknown> }

function eventText(e: RunnerEvent): string {
  const p = e.payload ?? {};
  // tool_call inputs matter too: migration Writes, lint commands
  if (e.kind === "tool_call") return JSON.stringify(p.input ?? "");
  return unquoteJSON(String(p.output ?? p.content ?? p.text ?? p.message ?? ""));
}

// useChecks digs the freshest evidence for every check out of session events
// (newest-first; counted/delta evidence beats prose) and card comments.
export function useChecks(cardId: string, comments: CardEvent[]): Checks {
  const empty: Checks = { tests: null, coverage: null, lint: null, version: null, migrations: [] };
  const [checks, setChecks] = useState<Checks>(empty);

  useEffect(() => {
    let stale = false;
    (async () => {
      let testsProse: TestStatus | null = null;
      const acc: Checks = { tests: null, coverage: null, lint: null, version: null, migrations: [] };
      const done = () =>
        acc.tests != null && acc.coverage?.before != null && acc.lint != null && acc.version != null;
      const consider = (text: string) => {
        const t = unquoteJSON(text);
        const ts = extractTestStatus(t);
        if (ts) {
          if (ts.counted) acc.tests = acc.tests ?? ts;
          else testsProse = testsProse ?? ts;
        }
        const c = extractCoverage(t);
        if (c && (acc.coverage == null || (acc.coverage.before == null && c.before != null))) acc.coverage = c;
        acc.lint = acc.lint ?? extractLint(t);
        acc.version = acc.version ?? extractVersion(t);
        for (const f of extractMigrations(t)) if (!acc.migrations.includes(f)) acc.migrations.push(f);
      };
      try {
        const sessions = await api<RunnerSession[]>(`/api/cards/${cardId}/runner-sessions`);
        const current = sessions?.[sessions.length - 1];
        if (current) {
          const events = await api<RunnerEvent[]>(`/api/runner-sessions/${current.id}/events`);
          for (const e of (events ?? []).slice().reverse()) {
            consider(eventText(e));
            if (done()) break;
          }
        }
      } catch { /* no session — comments may still know */ }
      for (const c of comments.slice().reverse()) {
        consider(String(c.data?.text ?? ""));
        if (done()) break;
      }
      acc.tests = acc.tests ?? testsProse;
      acc.migrations.sort();
      if (!stale) setChecks(acc);
    })();
    return () => { stale = true; };
    // `comments` is a fresh array on every render; only a change in how many
    // there are should rescan, so the length is the dependency on purpose.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cardId, comments.length]);

  return checks;
}

// ── chips (header row) ───────────────────────────────────────────────────────

export function TestChip({ status }: { status: TestStatus | null }) {
  if (!status) return null;
  return (
    <span className={`chip tests ${status.passed ? "pass" : "fail"}`}>
      {status.passed ? "✓" : "✗"} {status.detail}
    </span>
  );
}

export function CoverageChip({ cov }: { cov: CoverageStatus | null }) {
  if (!cov) return null;
  const delta = cov.before != null ? Math.round((cov.pct - cov.before) * 10) / 10 : null;
  const dir = delta == null ? "flat" : delta >= 0 ? "up" : "down";
  return (
    <span className={`chip cov ${dir}`} title={cov.approx ? "averaged across packages" : undefined}>
      cov {cov.approx ? "~" : ""}{cov.pct}%{delta != null && ` ${delta >= 0 ? "↑" : "↓"}${Math.abs(delta)}`}
    </span>
  );
}

export function LintChip({ lint }: { lint: LintStatus | null }) {
  if (!lint) return null;
  return (
    <span className={`chip tests ${lint.passed ? "pass" : "fail"}`}>
      {lint.passed ? "✓" : "✗"} lint {lint.detail}
    </span>
  );
}

// ── review-box pills — same facts, stated where the verdict is made ──────────

export function ChecksRow({ checks }: { checks: Checks }) {
  const { tests, coverage, lint, version, migrations } = checks;
  if (!tests && !coverage && !lint && !version && migrations.length === 0) return null;
  const covDelta = coverage?.before != null ? Math.round((coverage.pct - coverage.before) * 10) / 10 : null;
  return (
    <div className="row checks-row">
      {tests && (
        <p className={`checks ${tests.passed ? "pass" : "fail"}`}>
          {tests.passed ? "✓" : "✗"} {tests.detail}
        </p>
      )}
      {lint && (
        <p className={`checks ${lint.passed ? "pass" : "fail"}`}>
          {lint.passed ? "✓" : "✗"} lint {lint.detail}
        </p>
      )}
      {coverage && (
        <p className={`checks ${covDelta == null ? "flat" : covDelta >= 0 ? "pass" : "fail"}`}>
          cov {coverage.approx ? "~" : ""}{coverage.pct}%
          {covDelta != null && ` ${covDelta >= 0 ? "↑" : "↓"}${Math.abs(covDelta)} (was ${coverage.before}%)`}
        </p>
      )}
      {version && <p className="checks flat">v{version}</p>}
      {migrations.map((f) => (
        <p className="checks flat mono" key={f}>⛁ {f}</p>
      ))}
    </div>
  );
}
