import { IconButton } from '@blerglab/chat'
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "./api";
import { TimeAgo } from "./TimeAgo";
import type { CardCommitSummary } from './api';

interface GitInfo {
  sessions?: {id: string; name: string}[];
  session_id?: string;
  commits_error?: string;
  branch?: string;
  base?: string;
  commits?: {sha: string; message: string; author: string; date: string}[];
  members?: {id: string; name: string}[];
  generation?: number;
  launch_generation?: string;
}

interface GitPanelProps { cardId: string; boardId: string; cardNumber: number; summary?: CardCommitSummary[] }
export function GitPanel(props: GitPanelProps) {
  return <LocalGitPanel key={props.cardId} {...props} />;
}
function LocalGitPanel({ cardId, boardId, cardNumber, summary = [] }: GitPanelProps) {
  const [open, setOpen] = useState(false);
  const [info, setInfo] = useState<GitInfo | null>(null);
  const [selection, setSelection] = useState<GitInfo | null>(null);
  const [member, setMember] = useState('');
  const [session, setSession] = useState('');
  const [refs, setRefs] = useState({branch:'',base:''});
  const [draft, setDraft] = useState({branch:'',base:''});
  const [sessions, setSessions] = useState<{id: string; name: string}[]>([]);
  const [error, setError] = useState('');
  const [retry, setRetry] = useState(0);
  const params = new URLSearchParams();
  if (session) params.set('session_id',session);
  if (refs.branch) params.set('branch',refs.branch);
  if (refs.base) params.set('base',refs.base);
  if (member && selection) {params.set('member_id',member);params.set('generation',String(selection.generation));params.set('launch_generation',selection.launch_generation ?? '');}
  const query = params.size ? `?${params}` : '';
  useEffect(() => {
    if (!open) return;
    let active = true;
    api<GitInfo>(`/api/cards/${cardId}/git${query}`).then(reply => {
      if (!active) return;
      setInfo(reply); setError(reply.commits_error ?? '');
      if (reply.sessions) setSessions(reply.sessions);
      if (reply.session_id) setSession(current => current || reply.session_id || '');
      if (reply.members) setSelection(reply);
    }).catch(e => { if (active) {setError(e instanceof Error ? e.message : 'Could not read local Git.'); setInfo(null);} });
    return () => {active = false;};
  }, [open, cardId, query, retry]);
  return <section className={`git-section${open ? " open" : ""}`}>
    <button className="git-head" onClick={() => setOpen(!open)} aria-expanded={open}>
      <span className="glyph">⎇</span><span className="git-title">Git</span><span className="chev">{open ? "▾" : "▸"}</span>
    </button>
    {open && <div className="git-body">
      {summary.length > 0 && <section aria-label="Saved commit summary">
        <h4>Saved on this card</h4>
        <p className="dimtext">Full diffs require the workspace owner and a reachable desktop with the repository. Ended workspaces cannot serve full diffs when their checkout has been removed. An offline desktop or missing Git objects can also make a diff unavailable; the error below gives the specific reason.</p>
        {summary.map((s, index) => {
          const savedQuery = new URLSearchParams({session_id:s.session_id, branch:s.branch, base:s.base});
          if (s.member_id) {
            savedQuery.set('member_id',s.member_id);
            savedQuery.set('generation',String(s.generation));
            savedQuery.set('launch_generation',s.launch_generation ?? '');
          }
          return <section key={index}>
            <h4 style={{overflowWrap:'anywhere'}}>{s.repository}</h4>
            <Link className="btn ghost small" to={`/boards/${boardId}/cards/${cardNumber}/diff?${savedQuery}`}>Open full diff</Link>
            {s.truncated && <p className="dimtext">This summary is truncated.</p>}
            {!s.commits.length && <p className="dimtext">No commits recorded.</p>}
            {s.commits.map(c => <div className="commit" key={c.sha}>
              <span className="mono sha" title={c.sha}>{c.sha.slice(0,7)}</span><span className="cmsg">{c.message}</span>
              <span className="cwho">{c.author} · <TimeAgo iso={c.date}/></span>
            </div>)}
            {!s.files.length && <p className="dimtext">No changed files recorded.</p>}
            <ul>{s.files.map(f => <li key={f.path} style={{overflowWrap:'anywhere'}}>
              <code>{f.old_path ? `${f.old_path} → ` : ''}{f.path}</code>{' '}
              <span>{f.binary ? 'Binary' : `+${f.adds} −${f.dels}`}</span>
            </li>)}</ul>
          </section>;
        })}
      </section>}
      {error ? <><p role="alert">{error}</p><IconButton label="Retry" labeled onClick={() => setRetry(n => n+1)} /></> : !info && <p className="dimtext">Loading local Git…</p>}
      {sessions.length > 1 && <label>Workspace <select aria-label="Git workspace" value={session || info?.session_id || sessions[0]?.id} onChange={e => {setSession(e.target.value);setRefs({branch:'',base:''});setDraft({branch:'',base:''});setMember('');setSelection(null);setInfo(null);}}>
        {sessions.map((s,i) => <option key={s.id} value={s.id}>{s.name} {i+1}</option>)}
      </select></label>}
      {selection?.members && <label>Repository <select aria-label="Git repository" value={member} onChange={e => {setInfo(null);setMember(e.target.value);}}>
        <option value="">Choose a repository</option>
        {selection.members.map(m => <option key={m.id} value={m.id}>{m.name}</option>)}
      </select></label>}
      <form onSubmit={e => {e.preventDefault();setRefs(draft);setInfo(null);setRetry(n => n+1);}} style={{display:'flex',flexWrap:'wrap',gap:8}}>
          <label>Branch <input aria-label="Git branch" value={draft.branch} placeholder={info?.branch ?? "Work branch"} onChange={e => setDraft({...draft,branch:e.target.value})} style={{width:160,maxWidth:'100%'}}/></label>
          <label>Base <input aria-label="Git base branch" value={draft.base} placeholder="Default branch" onChange={e => setDraft({...draft,base:e.target.value})} style={{width:160,maxWidth:'100%'}}/></label>
          <button className="btn small" type="submit">Apply</button>
        </form>
      {info?.branch && <>
        <dl className="kv git-kv"><dt>Branch</dt><dd className="mono">{info.branch}</dd><dt>Diff</dt><dd>
          <Link className="btn ghost small" to={`/boards/${boardId}/cards/${cardNumber}/diff${query}`}>View in blerg-board</Link>
        </dd></dl>
        <div className="commits"><span className="commits-label">Commits</span>
          <p className="dimtext">Showing up to 30 recent commits. Long subjects and author names are shortened.</p>
          {!info.commits?.length && <p className="dimtext">No commits beyond the base branch.</p>}
          {info.commits?.map(c => <div className="commit" key={c.sha}>
            <span className="mono sha" title={c.sha}>{c.sha.slice(0,7)}</span><span className="cmsg">{c.message}</span>
            <span className="cwho">{c.author} · <TimeAgo iso={c.date}/></span>
          </div>)}
        </div>
      </>}
    </div>}
  </section>;
}
