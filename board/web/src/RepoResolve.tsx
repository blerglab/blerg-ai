import { useState } from "react";
import { api, Board } from "./api";

// RepoResolve: a board repo given as a bare desktop folder name ("app") cannot run on the cluster,
// which clones owner/name. The person picks the desktop this folder lives on, sees "app → owner/app"
// from that desktop's git remote, and saves owner/name once. Desktops have no owner in the runner,
// so the board cannot tell whose a desktop is: it asks only the one picked, and the confirmation is
// the person's. A folder with no remote stays a desktop-only repo.

export interface Desktop { daemon_id: string; name: string }

interface Remote { repo: string; daemon_id: string; desktop_name: string; has_folder: boolean; full_name?: string; problem?: string }

function RepoRow({ board, repo, desktops, onChanged }: { board: Board; repo: string; desktops: Desktop[]; onChanged?: () => void }) {
  // The board's own desktop target is preset; otherwise nothing is asked until the person picks.
  const preset = desktops.some(d => d.daemon_id === board.run_daemon_id) ? board.run_daemon_id || "" : "";
  const [daemon, setDaemon] = useState(preset);
  const [found, setFound] = useState<Remote | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function lookUp() {
    setBusy(true); setError(""); setFound(null);
    try {
      setFound(await api<Remote>(`/api/boards/${board.id}/repo-remote?${new URLSearchParams({ repo, daemon_id: daemon })}`));
    } catch (e) { setError(e instanceof Error ? e.message : "Could not ask that desktop."); }
    finally { setBusy(false); }
  }
  async function save(fullName: string) {
    setBusy(true); setError("");
    try {
      await api(`/api/boards/${board.id}/repos/resolve`, { method: "POST", json: { repo, daemon_id: daemon, full_name: fullName } });
      onChanged?.();
    } catch (e) { setError(e instanceof Error ? e.message : "Could not save the repo."); }
    finally { setBusy(false); }
  }
  return (
    <li className="repo-resolve-row">
      <p className="pop-foot warn"><span className="mono">{repo}</span> is a folder name on a desktop. The cluster clones owner/name: pick the desktop this folder lives on and look up its remote.</p>
      <div className="repo-resolve-controls">
        <select aria-label={`Desktop for ${repo}`} value={daemon} disabled={busy} onChange={e => { setDaemon(e.target.value); setFound(null); }}>
          <option value="">Pick the desktop this folder lives on…</option>
          {desktops.map(d => <option key={d.daemon_id} value={d.daemon_id}>{d.name}</option>)}
        </select>
        <button className="btn small" type="button" disabled={busy || !daemon} onClick={() => void lookUp()}>Look up {repo}</button>
      </div>
      {found?.full_name && <div className="repo-resolve-controls" role="status">
        <span className="mono">{repo} → {found.full_name}</span>
        <button className="btn small" type="button" disabled={busy} onClick={() => void save(found.full_name!)}>Save {found.full_name}</button>
      </div>}
      {found?.problem && <p className="pop-foot" role="status">{found.problem}</p>}
      {error && <p className="pop-foot warn" role="alert">{error}</p>}
    </li>
  );
}

export default function RepoResolve({ board, repos, desktops, onChanged }: { board: Board; repos: string[]; desktops: Desktop[]; onChanged?: () => void }) {
  if (repos.length === 0) return null;
  return (
    <div className="repo-resolve">
      <p className="sign pop-head">Repos for the cluster</p>
      {desktops.length === 0
        ? <p className="pop-foot warn">{repos.join(", ")} {repos.length === 1 ? "is a folder name" : "are folder names"} on a desktop. Connect the desktop {repos.length === 1 ? "this folder lives" : "these folders live"} on to look up {repos.length === 1 ? "its" : "their"} owner/name, or run on a desktop.</p>
        : <ul>{repos.map(repo => <RepoRow key={repo} board={board} repo={repo} desktops={desktops} onChanged={onChanged} />)}</ul>}
    </div>
  );
}
