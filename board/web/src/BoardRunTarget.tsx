import { useEffect, useState } from "react";
import { api, Board } from "./api";
import RepoResolve, { Desktop } from "./RepoResolve";

interface Target { runtime: string; daemon_id?: string; label: string; desktop_name?: string; unavailable?: string }
interface Targets { can_choose: boolean; targets: Target[]; default_runtime: string; default_daemon_id?: string; reason?: string; unresolved_repos?: string[] }

// The connected desktops, once each, for picking the person's own.
function desktopsOf(targets: Target[]): Desktop[] {
  const out: Desktop[] = [];
  for (const t of targets) {
    if (t.daemon_id && !t.unavailable && !out.some(d => d.daemon_id === t.daemon_id)) out.push({ daemon_id: t.daemon_id, name: t.desktop_name || t.daemon_id });
  }
  return out;
}
export default function BoardRunTarget({ board, editable, onChanged }: { board: Board; editable: boolean; onChanged?: () => void }) {
  const stored = `${board.run_runtime || ""}|${board.run_daemon_id || ""}`;
  const [choice, setChoice] = useState(stored);
  const [seen, setSeen] = useState(stored);
  if (seen !== stored) { setSeen(stored); setChoice(stored); }
  const [targets, setTargets] = useState<Targets | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [attempt, setAttempt] = useState(0);
  // A host target needs the owner's explicit acknowledgement each time it is chosen.
  const [hostAck, setHostAck] = useState(false);
  const isHost = choice.startsWith("daemon|");
  const hostName = targets?.targets.find(t => `${t.runtime}|${t.daemon_id || ""}` === choice)?.desktop_name || choice.split("|")[1] || "that desktop";
  useEffect(() => {
    if (!editable) return;
    let active = true;
    const controller = new AbortController();
    const timer = setTimeout(() => { controller.abort(); if (active) setError("Where this board runs could not be loaded. Retry."); }, 8000);
    void api<Targets>(`/api/boards/${board.id}/run-targets`, { signal: controller.signal }).then(value => {
      if (!Array.isArray(value.targets)) throw new Error("Invalid run targets");
      if (active) { setTargets(value); setError(""); }
    }).catch(() => { if (active) setError("Where this board runs could not be loaded. Retry."); }).finally(() => clearTimeout(timer));
    return () => { active = false; clearTimeout(timer); controller.abort(); };
  }, [board.id, editable, attempt]);
  async function save() {
    const [runtime, daemon] = choice.split("|");
    setBusy(true); setError("");
    try {
      const json: Record<string, unknown> = { run_runtime: runtime, run_daemon_id: daemon || "" };
      if (runtime === "daemon") json.run_host_acknowledged = hostAck;
      await api(`/api/boards/${board.id}`, { method: "PATCH", json });
      setHostAck(false);
      onChanged?.();
    } catch (e) { setError(e instanceof Error ? e.message : "Could not save where this board runs."); }
    finally { setBusy(false); }
  }
  return <>
    <dt>Where it runs</dt><dd>
      {editable && targets?.can_choose ? <>
        <select style={{ maxWidth: "100%" }} aria-label="Where this board runs" value={choice} disabled={busy} onChange={e => { setChoice(e.target.value); setHostAck(false); }}>
          <option value="|">Automatic{!board.run_runtime && targets.default_runtime ? ` (${targets.default_runtime === "docker" ? "Local sandbox" : targets.default_runtime === "daemon" ? "This machine" : "Cluster"})` : ""}</option>
          {targets.targets.map(t => <option key={`${t.runtime}|${t.daemon_id || ""}`} value={`${t.runtime}|${t.daemon_id || ""}`} disabled={!!t.unavailable}>{t.label}{t.unavailable ? ` — unavailable: ${t.unavailable}` : ""}</option>)}
        </select>{" "}
        <button className="btn small" type="button" disabled={busy || (isHost && !hostAck)} onClick={() => void save()}>{busy ? "Saving…" : "Save where it runs"}</button>
        {isHost && <label className="pop-foot warn" style={{ display: "flex", gap: 8, alignItems: "flex-start" }}>
          <input type="checkbox" checked={hostAck} disabled={busy} onChange={e => setHostAck(e.target.checked)} />
          <span>I understand that this board’s sessions, card runs, and automation will run on {hostName} as the person who runs that desktop, not as me: unattended, without a sandbox, with that person’s files and credentials. Anyone who can write cards on this board can put prompts there.</span>
        </label>}
        {isHost && board.run_runtime === "daemon" && `daemon|${board.run_daemon_id || ""}` === choice && board.run_host_acknowledged_at && <p className="pop-foot">Acknowledged {new Date(board.run_host_acknowledged_at).toLocaleString()}.</p>}
        <p className="pop-foot">Applies to board sessions, card runs, and automation. A single run can override it. On a desktop-only install, Automatic uses that desktop.</p>
        <RepoResolve board={board} repos={targets.unresolved_repos ?? []} desktops={desktopsOf(targets.targets)} onChanged={() => { setAttempt(x => x + 1); onChanged?.(); }} />
      </> : <span>{board.run_runtime ? `${board.run_daemon_id ? `${board.run_daemon_id} · ` : ""}${board.run_runtime}` : "Automatic"}{targets?.reason ? ` — ${targets.reason}` : ""}</span>}
      {error && <p role="alert">{error} <button type="button" onClick={() => setAttempt(x => x + 1)}>Retry run targets</button></p>}
    </dd>
  </>;
}
