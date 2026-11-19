import { ReleaseBurn, MilestoneBurn, type BurnChart } from "./ReleaseBurn";
import { useCallback, useEffect, useState } from "react";
import { matchesFilter, type BoardFilter } from "./boardFilter";
import { api, pendingBlockers, type Card, type Column } from "./api";

export interface Release { id: string; board_id: string; name: string; target_date: string | null; released_at?: string | null; released_by?: string | null; status: "planned" | "in progress" | "released" }
interface ReleaseView { burn?: BurnChart; release: Release; cards: Card[]; done: number; done_card_ids?: string[]; restored_card_ids?: string[]; by_column: Record<string, number> }

export function CardRelease({ card, onChange }: { card: Card; onChange: (card: Card) => void }) {
  const [releases, setReleases] = useState<Release[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => { let live = true; api<Release[]>(`/api/boards/${card.board_id}/releases`).then(r => { if (live) setReleases(r); }).catch(e => { if (live) setError(String(e)); }); return () => { live = false; }; }, [card.board_id]);
  const closed = releases.some(r => r.id === card.release_id && r.status === "released");
  async function save(value: string) {
    setBusy(true); setError("");
    try { onChange(await api<Card>(`/api/cards/${card.id}`, { method: "PATCH", body: JSON.stringify({ release_id: value, if_match: card.version }) })); }
    catch (e) { setError(String(e)); } finally { setBusy(false); }
  }
  return <div className="estimate-editor"><label>Release
    <select aria-label="Card release" disabled={busy || closed} value={card.release_id ?? ""} onChange={e => void save(e.target.value)}>
      <option value="">No release</option>
      {releases.filter(r => r.status !== "released" || r.id === card.release_id).map(r => <option key={r.id} value={r.id}>{r.name}</option>)}
    </select>
  </label>{error && <p role="alert">{error}</p>}</div>;
}

export function ReleasePlanning({ boardId, readOnly, selected, onFilter, grouped, onGroup, onOpenCard, revision, filter, columns }: {
  boardId: string; readOnly: boolean; selected: string; onFilter: (id: string) => void;
  filter?: BoardFilter; columns?: Column[]; revision?: Card[]; grouped: boolean; onGroup: (value: boolean) => void; onOpenCard: (card: Card) => void;
}) {
  const [releases, setReleases] = useState<Release[]>([]);
  const [cards, setCards] = useState<Card[]>([]);
  const [expanded, setExpanded] = useState(false);
  const [view, setView] = useState<ReleaseView | null>(null);
  const [viewID, setViewID] = useState("");
  const [search, setSearch] = useState("");
  const [name, setName] = useState("");
  const [date, setDate] = useState("");
  const [status, setStatus] = useState("planned");
  const [editing, setEditing] = useState("");
  const [next, setNext] = useState("");
  const [confirmCut, setConfirmCut] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const base = `/api/boards/${boardId}/releases`;
  const refresh = useCallback(async () => {
    try { const r = await api<Release[]>(base); setReleases(r); }
    catch (e) { setError(String(e)); }
  }, [base]);
  useEffect(() => {
    let live = true;
    api<Release[]>(base).then(r => { if (live) setReleases(r); }).catch(e => { if (live) setError(String(e)); });
    return () => { live = false; };
  }, [base, revision]);
  useEffect(() => {
    if (!grouped) return;
    let live = true;
    const load = () => { api<Card[]>(`/api/boards/${boardId}/cards`).then(c => { if (live) setCards(c); }).catch(e => { if (live) setError(String(e)); }); };
    load();
    return () => { live = false; };
  }, [boardId, grouped, revision]);
  useEffect(() => {
    if (!viewID) return;
    let live = true;
    api<ReleaseView>(`${base}/${viewID}`).then(v => { if (live) setView(v); }).catch(e => { if (live) setError(String(e)); });
    return () => { live = false; };
  }, [base, viewID, revision, releases]);
  function open(id: string) { setError(""); setSearch(""); setNext(""); setViewID(id); setConfirmCut(false); }
  async function save() {
    setBusy(true); setError("");
    try {
      await api(`${base}${editing ? `/${editing}` : ""}`, { method: editing ? "PATCH" : "POST", body: JSON.stringify({ name, target_date: date || null, status }) });
      setName(""); setDate(""); setStatus("planned"); setEditing(""); await refresh();
    } catch (e) { setError(String(e)); } finally { setBusy(false); }
  }
  async function cut() {
    if (!view) return;
    setBusy(true); setError("");
    try {
      await api(`${base}/${view.release.id}/close`, { method: "POST", body: JSON.stringify({ next_release_id: next }) });
      await refresh(); setNext(""); setConfirmCut(false);
    } catch (e) { setError(String(e)); } finally { setBusy(false); }
  }
  const archiveCount = view?.cards.filter(c => !c.archived_at && view.done_card_ids?.includes(c.id)).length ?? 0;
  function edit(r: Release) { setEditing(r.id); setName(r.name); setDate(r.target_date ?? ""); setStatus(r.status); }
  function cardButton(c: Card) { return <button type="button" className="btn ghost small release-card" key={c.id} onClick={() => onOpenCard(c)}>#{c.number} {c.title}{columns?.find(col => col.id === c.column_id) ? ` · ${columns.find(col => col.id === c.column_id)?.name}` : ""}{pendingBlockers(c).length > 0 || c.stuck_at ? " · Blocked" : ""}{c.merged_sha ? ` · ${c.merged_sha.slice(0, 7)}` : ""}</button>; }
  const groupedReleases = [...releases.filter(r => r.status !== "released"), { id: "none", name: "No release", status: "planned", target_date: null }];
  return <section className="release-planning" aria-label="Release planning">
    <div className="release-toolbar">
      <label>Release <select aria-label="Filter by release" value={selected} onChange={e => onFilter(e.target.value)}>
        <option value="">All releases</option><option value="none">No release</option>
        {releases.map(r => <option key={r.id} value={r.id}>{r.name}</option>)}
      </select></label>
      <button type="button" className="btn ghost small" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>Releases</button>
      <button type="button" className="btn ghost small" aria-pressed={grouped} onClick={() => onGroup(!grouped)}>Group by release</button>
    </div>
    {error && <p role="alert">{error}</p>}
    {expanded && <div className="release-workspace">
      <MilestoneBurn key={boardId} boardId={boardId} />
      <div className="release-list">{releases.length === 0 && <p>No releases yet.</p>}{releases.map(r => <div key={r.id}>
        <button type="button" className="btn ghost small" onClick={() => void open(r.id)}>{r.name}</button> <span>{r.status}{r.target_date ? ` · ${r.target_date}` : ""}</span>
        {!readOnly && r.status !== "released" && <button type="button" className="btn ghost small" aria-label={`Edit ${r.name}`} onClick={() => edit(r)}>Edit</button>}
      </div>)}</div>
      {!readOnly && <form className="release-form" onSubmit={e => { e.preventDefault(); void save(); }}>
        <label>Name<input aria-label="Release name" required maxLength={120} value={name} onChange={e => setName(e.target.value)} /></label>
        <label>Target date<input aria-label="Target date" type="date" value={date} onChange={e => setDate(e.target.value)} /></label>
        <label>Status<select aria-label="Release status" value={status} onChange={e => setStatus(e.target.value)}><option>planned</option><option>in progress</option></select></label>
        <button type="submit" className="btn small primary" disabled={busy}>{editing ? "Save release" : "Add release"}</button>
        {editing && <button type="button" className="btn ghost small" onClick={() => { setEditing(""); setName(""); setDate(""); setStatus("planned"); }}>Cancel edit</button>}
      </form>}
      {view && view.release.id === viewID && <section className="release-record" aria-label="Release record">
        <h3>{view.release.name} · {view.release.status}</h3>
        <p>{view.done} of {view.cards.length - (view.restored_card_ids?.length ?? 0)} done</p>
        {view.release.released_at && <p>Released {view.release.released_at}{view.release.released_by ? ` by ${view.release.released_by}` : ""}</p>}
        {!!view.restored_card_ids?.length && <div><h4>Restored</h4><p>These cards were restored and detached from this release.</p>{view.cards.filter(c => view.restored_card_ids?.includes(c.id)).map(cardButton)}</div>}
        {view.burn && <ReleaseBurn burn={view.burn} />}
        <div className="release-progress">{Object.entries(view.by_column).map(([column, count]) => <span className="chip" key={column}>{column}: {count}</span>)}</div>
        <label>Search release<input aria-label="Search release" type="search" value={search} onChange={e => setSearch(e.target.value)} /></label>
        {[false, true].map(done => <div key={String(done)}><h4>{done ? "Done" : "Open"}</h4><div className="release-cards">{view.cards.filter(c => !view.restored_card_ids?.includes(c.id) && (view.done_card_ids?.includes(c.id) ?? !!c.archived_at) === done && `${c.number} ${c.title} ${c.body ?? ""}`.toLowerCase().includes(search.toLowerCase())).map(cardButton)}</div></div>)}
        {!readOnly && view.release.status !== "released" && <div className="release-toolbar">
          <label>Next release<select aria-label="Next release" value={next} onChange={e => setNext(e.target.value)}><option value="">Choose next release</option>{releases.filter(r => r.id !== view.release.id && r.status !== "released").map(r => <option key={r.id} value={r.id}>{r.name}</option>)}</select></label>
          <button type="button" className="btn small primary" disabled={busy || (view.done < view.cards.length && !next)} onClick={() => setConfirmCut(true)}>Cut release</button>
          {confirmCut && <div role="alertdialog" aria-label="Confirm release cut"><p>Cut {view.release.name}? Archive {archiveCount} Done cards and move {view.cards.length - view.done} open cards to {releases.find(r => r.id === next)?.name ?? "no next release"}.</p><button type="button" className="btn primary small" disabled={busy} onClick={() => void cut()}>Confirm cut</button><button type="button" className="btn ghost small" onClick={() => setConfirmCut(false)}>Cancel</button></div>}
          <button type="button" className="btn danger small" disabled={busy} onClick={() => { void api(`${base}/${view.release.id}`, {method:"DELETE"}).then(async () => { setViewID(""); setView(null); await refresh(); }).catch(e => setError(String(e))); }}>Delete release</button>
          <p>Archive Done cards in this release and move open cards to the next release.</p>
        </div>}
      </section>}
    </div>}
    {grouped && <div className="release-groups">{groupedReleases.filter(r => !selected || r.id === selected).map(r => <section key={r.id}><h3>{r.name}</h3><div className="release-cards">{cards.filter(c => (c.release_id ?? "none") === r.id && (!filter || matchesFilter(c, filter))).map(cardButton)}</div></section>)}</div>}
  </section>;
}
