import { useEffect, useState } from "react";
import { api, Board } from "./api";

// Board metrics: the board's life in numbers. Charts are hand-rolled SVG
// bars — no chart library for four bar charts.

interface DayRow {
  day: string; role?: string; backend?: string; model?: string;
  count: number; seconds?: number; tokens_in?: number; tokens_out?: number; avg_ms?: number;
}
interface Metrics {
  sessions: DayRow[];
  tokens_by_model: DayRow[];
  cards_completed: DayRow[];
  gate: DayRow[];
  rollup: {
    sessions: number; agent_seconds: number; tokens_in: number; tokens_out: number;
    cards_completed: number; gate_calls: number; merges: number; since: string | null;
  };
}

function dur(secs: number): string {
  if (secs < 90) return `${secs}s`;
  if (secs < 5400) return `${Math.round(secs / 60)}m`;
  return `${(secs / 3600).toFixed(1)}h`;
}
function tok(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${Math.round(n / 1e3)}k`;
  return String(n);
}

// daily series across the full date range, filling gaps with zero
function seriesOf(rows: DayRow[], value: (d: DayRow) => number, split?: (d: DayRow) => string) {
  const byDay = new Map<string, Map<string, number>>();
  for (const r of rows) {
    const k = split ? split(r) : "all";
    if (!byDay.has(r.day)) byDay.set(r.day, new Map());
    const m = byDay.get(r.day)!;
    m.set(k, (m.get(k) ?? 0) + value(r));
  }
  const days = [...byDay.keys()].sort();
  if (days.length === 0) return { days: [] as string[], keys: [] as string[], data: byDay };
  // fill the range
  const all: string[] = [];
  const d0 = new Date(days[0] + "T00:00:00Z"), d1 = new Date(days[days.length - 1] + "T00:00:00Z");
  for (let d = d0; d <= d1; d = new Date(d.getTime() + 86400e3)) {
    all.push(d.toISOString().slice(0, 10));
  }
  const keys = [...new Set(rows.map((r) => (split ? split(r) : "all")))].sort();
  return { days: all, keys, data: byDay };
}

const PALETTE: Record<string, string> = {
  worker: "var(--blaze)", reviewer: "var(--amber)", discuss: "var(--fog)",
  board: "var(--lichen)", all: "var(--blaze)", openai: "var(--lichen)", claude: "var(--blaze)",
};
// split keys outside PALETTE (model names, mainly) cycle through the same
// trail-signage tokens rather than falling back to one flat --fog-dim.
const FALLBACK_COLORS = ["var(--blaze)", "var(--lichen)", "var(--amber)", "var(--danger)", "var(--fog)"];
function colorFor(key: string, keys: string[]): string {
  if (PALETTE[key]) return PALETTE[key];
  const idx = keys.filter((k) => !PALETTE[k]).indexOf(key);
  return FALLBACK_COLORS[idx % FALLBACK_COLORS.length];
}

// niceCeil rounds up to 1/2/5 × 10^k so the y-axis tops out on a clean number.
function niceCeil(v: number): number {
  if (v <= 0) return 1;
  const mag = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 5, 10]) {
    if (v <= m * mag) return m * mag;
  }
  return 10 * mag;
}

function BarChart({ title, rows, value, split, fmt }: {
  title: string; rows: DayRow[];
  value: (d: DayRow) => number; split?: (d: DayRow) => string;
  fmt: (n: number) => string;
}) {
  const { days, keys, data } = seriesOf(rows, value, split);
  if (days.length === 0) return null;
  const totals = days.map((d) => keys.reduce((a, k) => a + (data.get(d)?.get(k) ?? 0), 0));
  if (totals.every((t) => t === 0)) return null;
  const yMax = niceCeil(Math.max(...totals, 1));

  // layout: fixed margins for axes; bars never stretch
  const ML = 46, MB = 20, MT = 16, plotH = 110;
  const bw = 26, gap = 8;
  const plotW = days.length * (bw + gap);
  const W = ML + plotW + 8, H = MT + plotH + MB;
  const y = (v: number) => MT + plotH - (v / yMax) * plotH;
  const showValues = days.length <= 16;
  // x labels: at most ~6, always first and last
  const step = Math.max(1, Math.ceil(days.length / 6));
  const ticks = [0, 0.5, 1].map((f) => f * yMax);

  return (
    <div className="metric-card">
      <div className="row" style={{ marginBottom: 4 }}>
        <h4>{title}</h4>
        <span className="spacer" />
        {keys.length > 1 && (
          <span className="metric-legend">
            {keys.map((k) => (
              <span key={k}><i style={{ background: colorFor(k, keys) }} />{k}</span>
            ))}
          </span>
        )}
      </div>
      <div className="metric-plot">
        <svg viewBox={`0 0 ${W} ${H}`} width={W} height={H} role="img" aria-label={title}>
          {/* gridlines + y labels */}
          {ticks.map((t) => (
            <g key={t}>
              <line x1={ML} x2={W - 4} y1={y(t)} y2={y(t)}
                stroke="var(--stone)" strokeWidth="1" strokeDasharray={t === 0 ? "" : "3 4"} />
              <text x={ML - 6} y={y(t) + 3} textAnchor="end" className="axis-label">{fmt(t)}</text>
            </g>
          ))}
          {days.map((d, i) => {
            const x0 = ML + i * (bw + gap);
            let yy = y(0);
            return (
              <g key={d}>
                {keys.map((k) => {
                  const v = data.get(d)?.get(k) ?? 0;
                  const h = (v / yMax) * plotH;
                  yy -= h;
                  return v > 0 ? (
                    <rect key={k} x={x0} y={yy} width={bw} height={h}
                      fill={colorFor(k, keys)} rx={2} />
                  ) : null;
                })}
                {showValues && totals[i] > 0 && (
                  <text x={x0 + bw / 2} y={yy - 4} textAnchor="middle" className="bar-value">
                    {fmt(totals[i])}
                  </text>
                )}
                {(i % step === 0 || i === days.length - 1) && (
                  <text x={x0 + bw / 2} y={H - 6} textAnchor="middle" className="axis-label">
                    {d.slice(5)}
                  </text>
                )}
              </g>
            );
          })}
        </svg>
      </div>
    </div>
  );
}

// MetricsModal: metrics as a sheet over the board — same experience as
// opening a card, no page navigation.
export function MetricsModal({ boardId, onClose }: { boardId: string; onClose: () => void }) {
  const [board, setBoard] = useState<Board | null>(null);
  const [m, setM] = useState<Metrics | null>(null);

  useEffect(() => {
    api<Board>(`/api/boards/${boardId}`).then(setBoard).catch(() => {});
    api<Metrics>(`/api/boards/${boardId}/metrics`).then(setM).catch(() => {});
  }, [boardId]);

  if (!board || !m) return null;
  const r = m.rollup;
  return (
    <div className="sheet-backdrop" onClick={onClose}>
    <div className="sheet metrics-sheet" onClick={(e) => e.stopPropagation()}
      role="dialog" aria-label={`${board.name} metrics`}>
      <div className="row" style={{ marginBottom: 14 }}>
        <h2 style={{ margin: 0 }}>{board.name} · metrics</h2>
        <span className="spacer" />
        <button className="btn ghost small" onClick={onClose}>Close</button>
      </div>

      <div className="rollups">
        <div className="rollup"><b>{r.sessions}</b><span>sessions{r.since ? ` since ${String(r.since).slice(0, 10)}` : ""}</span></div>
        <div className="rollup"><b>{dur(r.agent_seconds)}</b><span>agent time</span></div>
        <div className="rollup"><b>{tok(r.tokens_in + r.tokens_out)}</b><span>tokens{r.tokens_in + r.tokens_out > 0 ? ` (${tok(r.tokens_in)} in / ${tok(r.tokens_out)} out)` : ""}</span></div>
        <div className="rollup"><b>{r.cards_completed}</b><span>cards completed</span></div>
        <div className="rollup"><b>{r.merges}</b><span>merges</span></div>
        <div className="rollup"><b>{r.gate_calls}</b><span>gate calls</span></div>
      </div>

      <BarChart title="Sessions per day" rows={m.sessions}
        value={(d) => d.count} split={(d) => d.role ?? "all"} fmt={(n) => String(n)} />
      <BarChart title="Agent time per day" rows={m.sessions}
        value={(d) => d.seconds ?? 0} split={(d) => d.role ?? "all"} fmt={dur} />
      <BarChart title="Tokens per day" rows={m.sessions}
        value={(d) => (d.tokens_in ?? 0) + (d.tokens_out ?? 0)} split={(d) => d.role ?? "all"} fmt={tok} />
      {new Set(m.tokens_by_model.map((d) => d.model)).size > 1 && (
        <BarChart title="Tokens per day by model" rows={m.tokens_by_model}
          value={(d) => (d.tokens_in ?? 0) + (d.tokens_out ?? 0)} split={(d) => d.model ?? "unknown"} fmt={tok} />
      )}
      <BarChart title="Cards completed per day" rows={m.cards_completed}
        value={(d) => d.count} fmt={(n) => String(n)} />
      <BarChart title="Gate calls per day" rows={m.gate}
        value={(d) => d.count} split={(d) => d.backend ?? "unknown"} fmt={(n) => String(n)} />
    </div>
    </div>
  );
}
