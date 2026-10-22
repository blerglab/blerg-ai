// New board's repository picker: the repositories the connected desktops have checked out (asked of
// the runner through the board server, GET /api/repo-choices), several allowed, in the order picked:
// the first is the board's primary repository, the one its sessions start in. A repository no desktop
// has can be typed. Optional: a board with no repositories is fine.
import { useEffect, useMemo, useState } from "react";
import { api } from "./api";

interface RepoChoice { repo: string; folder: string; desktops: string[] }
interface Choices { available: boolean; repos: RepoChoice[]; error?: string }

// The same rule the server holds a board's repositories to: owner/name or a folder name.
const SEGMENT = "[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}";
const REPO = new RegExp(`^${SEGMENT}(/${SEGMENT})?$`);
export const validRepo = (r: string) => REPO.test(r) && !r.endsWith(".git");
export const MAX_REPOS = 20;

export function NewBoardRepos({ value, onChange }: { value: string[]; onChange: (repos: string[]) => void }) {
  const [choices, setChoices] = useState<Choices | null>(null);
  const [filter, setFilter] = useState("");
  const [typed, setTyped] = useState("");
  const [typedError, setTypedError] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    api<Choices>("/api/repo-choices")
      .then((c) => { if (live) setChoices(c && Array.isArray(c.repos) ? c : { available: false, repos: [] }); })
      .catch(() => { if (live) setChoices({ available: false, repos: [], error: "Could not list your desktops' repositories; type one instead." }); });
    return () => { live = false; };
  }, []);
  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return (choices?.repos ?? []).filter((c) => !q || c.repo.toLowerCase().includes(q) || c.folder.toLowerCase().includes(q));
  }, [choices, filter]);
  const has = (repo: string) => value.some((r) => r.toLowerCase() === repo.toLowerCase());
  const toggle = (repo: string) => onChange(has(repo) ? value.filter((r) => r.toLowerCase() !== repo.toLowerCase()) : value.length >= MAX_REPOS ? value : [...value, repo]);
  const add = () => {
    const repo = typed.trim();
    if (!repo) return;
    if (!validRepo(repo)) { setTypedError("Give owner/name or a folder name: letters, digits, _ . and -."); return; }
    if (value.length >= MAX_REPOS) { setTypedError(`At most ${MAX_REPOS} repositories.`); return; }
    setTypedError(null);
    setTyped("");
    if (!has(repo)) onChange([...value, repo]);
  };
  return (
    <fieldset className="new-board-repos">
      <legend>Repositories <span className="dim">(optional; the first is where the board's sessions start)</span></legend>
      {value.length > 0 && (
        <ol className="new-board-picked" aria-label="Chosen repositories">
          {value.map((r, i) => (
            <li key={r}>
              <span className="mono">{r}</span>
              {i === 0 && <span className="dim"> primary</span>}
              <button type="button" className="btn small" aria-label={`Remove ${r}`} onClick={() => toggle(r)}>×</button>
            </li>
          ))}
        </ol>
      )}
      {choices === null ? <p className="dim">Looking for your desktops' repositories…</p> : choices.repos.length > 0 ? (
        <>
          {choices.repos.length > 6 && (
            <input type="search" aria-label="Find a repository" placeholder="Find a repository" value={filter} onChange={(e) => setFilter(e.target.value)} />
          )}
          <ul className="new-board-choices" aria-label="Repositories on your desktops">
            {shown.map((c) => (
              <li key={c.repo}>
                <label>
                  <input type="checkbox" checked={has(c.repo)} onChange={() => toggle(c.repo)} />
                  <span className="mono">{c.repo}</span>
                  <span className="dim">{c.desktops.length ? ` on ${c.desktops.join(", ")}` : ""}</span>
                </label>
              </li>
            ))}
            {shown.length === 0 && <li className="dim">No repository matches.</li>}
          </ul>
        </>
      ) : (
        <p className="dim">{choices.error ?? (choices.available ? "Your connected desktops have no repositories checked out." : "No desktop is connected; type a repository instead.")}</p>
      )}
      <div className="new-board-add">
        <input type="text" aria-label="Add a repository by name" placeholder="owner/name" value={typed}
          onChange={(e) => { setTyped(e.target.value); setTypedError(null); }}
          onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add(); } }} />
        <button type="button" className="btn small" onClick={add} disabled={!typed.trim()}>Add</button>
      </div>
      {typedError && <p role="alert" className="new-board-error">{typedError}</p>}
    </fieldset>
  );
}
