import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "./api";
import { TimeAgo } from "./TimeAgo";
import { SafeAnchor } from "./CardLinks";

interface Commit { sha: string; message: string; author: string; date: string; url: string }
interface GitInfo {
  repo?: string;
  branch?: string;
  branch_url?: string;
  pr_url?: string;
  commits?: Commit[];
  commits_error?: string;
}

// GitPanel: everything version-control about a card as one delineated
// section — branch, PR, in-app diff, and commit history behind a toggle.
export function GitPanel({ cardId, boardId, cardNumber }: {
  cardId: string; boardId: string; cardNumber: number;
}) {
  const [open, setOpen] = useState(false);
  const [info, setInfo] = useState<GitInfo | null>(null);

  useEffect(() => {
    if (!open || info) return;
    api<GitInfo>(`/api/cards/${cardId}/git`).then(setInfo).catch(() => setInfo({}));
  }, [open, cardId, info]);

  return (
    <section className={`git-section${open ? " open" : ""}`}>
      <button className="git-head" onClick={() => setOpen(!open)} aria-expanded={open}>
        <span className="glyph">⎇</span>
        <span className="git-title">Git</span>
        <span className="chev">{open ? "▾" : "▸"}</span>
      </button>
      {open && (
        <div className="git-body">
          {!info && <p className="dimtext">loading…</p>}
          {info && !info.branch && !info.pr_url && (
            <p className="dimtext">No branch or PR on this card yet.</p>
          )}
          {(info?.branch || info?.pr_url) && (
            <dl className="kv git-kv">
              {info?.branch && (
                <>
                  <dt>branch</dt>
                  <dd>
                    <SafeAnchor className="mono" href={info.branch_url}>
                      {info.branch}
                    </SafeAnchor>
                  </dd>
                </>
              )}
              {info?.pr_url && (
                <>
                  <dt>pull request</dt>
                  <dd><SafeAnchor href={info.pr_url}>{info.pr_url}</SafeAnchor></dd>
                </>
              )}
              <dt>diff</dt>
              <dd>
                <Link className="btn ghost small" to={`/boards/${boardId}/cards/${cardNumber}/diff`}>
                  View in blerg-board
                </Link>
              </dd>
            </dl>
          )}
          {info?.commits_error && <p className="dimtext">{info.commits_error}</p>}
          {(info?.commits?.length ?? 0) > 0 && (
            <div className="commits">
              <span className="commits-label">commits</span>
              {info!.commits!.map((c) => (
                <div className="commit" key={c.sha}>
                  <SafeAnchor className="mono sha" href={c.url}>{c.sha}</SafeAnchor>
                  <span className="cmsg">{c.message}</span>
                  <span className="cwho">{c.author} · <TimeAgo iso={c.date} /></span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
