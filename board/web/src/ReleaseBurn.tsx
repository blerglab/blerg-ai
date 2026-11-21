import { api } from "./api";
import { useState } from "react";

export interface BurnPoint { date: string; scope: number; done: number; confirmed: number; added: number; finished: number }
export interface BurnChart { count: BurnPoint[]; size: BurnPoint[]; estimated: boolean; restored?: number; unavailable?: boolean; history_note?: string }

export function finishRange(points: BurnPoint[]): string {
 const last = points.at(-1);
 if (!last || last.scope === 0) return "No work in this release yet.";
 if (last.done >= last.scope) return "All work is done.";
 const first = points[Math.max(0, points.length - 8)];
 const days = (Date.parse(last.date) - Date.parse(first.date)) / 86400000;
 const slope = days > 0 ? (last.done - first.done) / days : 0;
 if (slope <= 0) return "A finish range needs more completed work.";
 const remaining = last.scope - last.done;
 const date = (speed: number) => new Date(Date.parse(last.date) + Math.ceil(remaining / speed) * 86400000).toISOString().slice(0, 10);
 return `Projected finish: ${date(slope * 1.5)} to ${date(slope * 0.5)}. Based on the last ${days} days at half to one and a half times the done rate, with no scope change.`;
}

export function ReleaseBurn({ burn, milestone = false }: { burn: BurnChart; milestone?: boolean }) {
 const [mode, setMode] = useState("up");
 const [selectedWeight, setWeight] = useState("count");
 const weight = burn.estimated ? selectedWeight : "count";
 const points = weight === "size" ? burn.size : burn.count;
 const max = Math.max(1, ...points.map(p => p.scope));
 const x = (i: number) => 40 + i * 700 / Math.max(1, points.length - 1);
 const y = (n: number) => 210 - n * 180 / max;
 const path = (value: (p: BurnPoint) => number) => points.map((p, i) => `${i ? "L" : "M"}${x(i)},${y(value(p))}`).join(" ");
 const down = mode === "down";
 return <section className="release-burn" aria-label={milestone ? "Milestone progress chart" : "Release progress chart"}>
  <h4>{milestone ? "Milestone progress" : "Release progress"}</h4>
  <div className="release-toolbar">
   <label>Chart type<select value={mode} onChange={e => setMode(e.target.value)}><option value="up">Burn-up</option><option value="down">Burn-down</option></select></label>
   <label>Weight<select value={weight} onChange={e => setWeight(e.target.value)}><option value="count">Card count</option>{burn.estimated && <option value="size">Size</option>}</select></label>
  </div>
  {weight === "size" && <p>Size weights: S = 1, M = 2, L = 4, XL = 8. Unestimated cards count as 1.</p>}
  {burn.unavailable ? <p role="status">Progress chart is unavailable. Try again later.</p> : <p>{finishRange(points)}</p>}
  {!!burn.restored && <p>{burn.restored} {burn.restored === 1 ? "card was" : "cards were"} restored after this release was cut. The chart shows work at the cut.</p>}
  {burn.history_note && <p>{burn.history_note}</p>}
  {points.length > 0 && <>
   <svg viewBox="0 0 780 300" role="img" aria-label={`${down ? "Burn-down" : "Burn-up"}: scope, ${down ? "remaining" : "done"}, and confirmed done. Daily added and finished bars. Dates are UTC. See the data table for exact values.`}>
    <text x="8" y="24" fill="currentColor">{max}</text><text x="8" y="214" fill="currentColor">0</text>
    <path d="M40,30V210H740" className="burn-axis" />
    <path d={path(p => p.scope)} className="burn-scope" />
    <path d={path(p => down ? p.scope - p.done : p.done)} className="burn-done" />
    <path d={path(p => down ? p.scope - p.confirmed : p.confirmed)} className="burn-confirmed" />
    {points.map((p,i) => <g key={p.date}>
     <rect x={x(i)-3} y={260-p.added*30/max} width="3" height={p.added*30/max} className="burn-added" />
     <rect x={x(i)} y={260-p.finished*30/max} width="3" height={p.finished*30/max} className="burn-finished" />
    </g>)}
    <text x="40" y="285" fill="currentColor">{points[0].date}</text><text x="740" y="285" textAnchor="end" fill="currentColor">{points.at(-1)?.date}</text>
   </svg>
   <p className="burn-legend"><span>Dates (UTC)</span><span>Scope (solid)</span><span>Done (dashed)</span><span>Confirmed done (dotted, excludes to-test)</span><span>Daily bars: added / finished</span></p>
  </>}
  <details><summary>Data table</summary><div className="burn-table"><table aria-label="Release progress data"><thead><tr>{["Date (UTC)","Scope","Done","Confirmed done","Remaining","Added","Finished"].map(h => <th key={h} scope="col">{h}</th>)}</tr></thead><tbody>{points.map(p => <tr key={p.date}><th scope="row">{p.date}</th><td>{p.scope}</td><td>{p.done}</td><td>{p.confirmed}</td><td>{p.scope-p.done}</td><td>{p.added}</td><td>{p.finished}</td></tr>)}</tbody></table></div></details>
 </section>;
}

export function MilestoneBurn({ boardId }: { boardId: string }) {
 const [tag,setTag] = useState("");
 const [chart,setChart] = useState<BurnChart | null>(null);
 const [error,setError] = useState("");
 const [busy,setBusy] = useState(false);
 async function load() {
  setBusy(true); setError(""); setChart(null);
  try { setChart(await api<BurnChart>(`/api/boards/${boardId}/burn?milestone=${encodeURIComponent(tag.trim())}`)); }
  catch(e) { setError(String(e)); } finally { setBusy(false); }
 }
 return <section aria-label="Milestone progress"><h4>Milestone progress</h4><p>Use a board tag to track a milestone.</p><form className="release-toolbar" onSubmit={e => {e.preventDefault();void load();}}><label>Milestone tag<input required maxLength={200} value={tag} onChange={e => setTag(e.target.value)} /></label><button type="submit" className="btn small primary" disabled={busy}>Show progress</button></form>{error && <p role="alert">{error}</p>}{chart && <ReleaseBurn burn={chart} milestone />}</section>;
}
