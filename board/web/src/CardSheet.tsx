import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, Board, boardSocket, Card, CardEvent, CardLink, Column, FieldDef, pendingBlockers } from "./api";
import { useMe } from "./me";
import { Conversation, MdText, useCardSessions } from "./Conversation";
import { GitPanel } from "./GitPanel";
import { StatsPanel } from "./StatsPanel";
import { TimeAgo } from "./TimeAgo";
import { Checks, ChecksRow, CoverageChip, LintChip, TestChip, useChecks } from "./Checks";

// FieldValue renders a declared custom field per the board's schema —
// blerg-board stays domain-ignorant; the board declares its own shape.
function FieldValue({ def, value }: { def: FieldDef; value: unknown }) {
  if (value == null) return null;
  const s = String(value);
  switch (def.display ?? (def.type === "url" ? "link" : "inline")) {
    case "hidden": return null;
    case "link": return <a href={s} target="_blank" rel="noreferrer">{s}</a>;
    case "badge": return <span className="chip" style={{ color: "var(--blaze)", borderColor: "var(--blaze-soft)" }}>{s}</span>;
    case "chip": return <span className="chip">{s}</span>;
    default: return <>{s}</>;
  }
}

// ReviewSection appears when the card sits in a review-ish column: the
// artifact to review (review-guide comment + PR links) and the two verdicts.
// onDone(notice) — a notice means the card moved somewhere other than done
// (an Accept that hit a merge conflict), so the sheet stays open and says so.
// Exported (only) so tests can exercise the Accept/Reject admin gate
// directly, without standing up the whole CardSheet (GitPanel, StatsPanel,
// Conversation, sessionStorage, etc.).
export function ReviewSection({ card, events, columns, deployURL, checks, onDone }: {
  card: Card; events: CardEvent[]; columns: Column[]; deployURL: string | null;
  checks: Checks; onDone: (notice?: string) => void;
}) {
  const me = useMe();
  const [feedback, setFeedback] = useState("");
  const [rejecting, setRejecting] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const { current, reviewer } = useCardSessions(card.id);

  const comments = events.filter((e) => e.type === "comment");
  // adversarial review state: a verdict comment is one that carries a Verdict
  // line. The prefix alone also matches blerg-board's own service breadcrumbs
  // ("Adversarial review session spawned…"), which are not rounds.
  const advComments = comments.filter((e) =>
    String(e.data?.text ?? "").startsWith("Adversarial review") &&
    /verdict:\s*(approve|request[- ]changes)/i.test(String(e.data?.text ?? "")),
  );
  const advComment = advComments[advComments.length - 1];
  const advVerdict = advComment
    ? (/verdict:\s*approve/i.test(String(advComment.data?.text ?? "")) ? "approve" : "request-changes")
    : null;
  const reviewerActive = reviewer != null &&
    !["stopped", "error"].includes(reviewer.lifecycle);
  const hasPR = (card.links ?? []).some((l) => l.kind === "pr");
  // mirrors reviewTarget() + maybeSpawnReviewer's repo guard in
  // internal/api/review.go: a PR link is the artifact when code was written,
  // otherwise a non-blank body is (spec/brainstorm cards). No repo means no
  // reviewer can ever spawn, so don't hold Accept hostage to a verdict.
  const reviewable = (card.repos ?? []).length > 0 &&
    (hasPR || (card.body ?? "").trim() !== "");
  const awaitingAdversarial = reviewable && advVerdict !== "approve";
  const guide = comments.slice().reverse().find((e) =>
    String(e.data?.text ?? "").toLowerCase().startsWith("review guide"),
  ) ?? comments[comments.length - 1];
  const prs = (card.links ?? []).filter((l) => l.kind === "pr");
  const doneCol = columns.find((c) => c.is_terminal) ??
    columns.find((c) => c.name.toLowerCase().includes("done"));
  const workCol = columns.find((c) => c.name.toLowerCase().includes("progress")) ?? columns[0];

  const accept = async () => {
    setBusy(true); setError("");
    try {
      // server-side: merges the PR (if any), deletes the branch, moves to done.
      // A PR that conflicts with main is not a failure — the server sends the
      // card back to a worker to resolve it and answers 202 "bounced".
      const out = await api<{ status?: string; note?: string }>(
        `/api/cards/${card.id}/accept`, { method: "POST" },
      );
      onDone(out?.status === "bounced"
        ? out.note || "The PR conflicts with main — nothing merged; the card went back to the worker."
        : undefined);
    } catch (e) {
      setError(e instanceof Error ? e.message : "accept failed");
    } finally { setBusy(false); }
  };

  const reject = async () => {
    if (!feedback.trim()) return;
    setBusy(true);
    try {
      await api(`/api/cards/${card.id}/comments`, {
        json: { text: "Changes requested: " + feedback.trim() },
      });
      // `current` falls back to the newest session of any role, which on a
      // spec card with no author session is the adversarial reviewer — it
      // neither wrote the work nor may edit it, so it is not a recipient.
      const author = current && current.role !== "reviewer" ? current : null;
      if (author && author.lifecycle !== "error") {
        const todo = hasPR
          ? "Address the feedback, update the PR, post a fresh 'Review guide' comment, and move the card back to review."
          : "Address the feedback by revising the card body, post a comment saying what you changed and why, and move the card back to review.";
        await api(`/api/runner-sessions/${author.id}/message`, {
          json: { text: "Human review came back with changes requested: " + feedback.trim() + "\n" + todo },
        }).catch(() => {});
      }
      if (workCol) await api(`/api/cards/${card.id}/move`, { json: { column_id: workCol.id } });
      onDone();
    } finally { setBusy(false); }
  };

  return (
    <section className="review-box">
      <h4>{awaitingAdversarial ? "In agent review" : "Ready for your review"}</h4>
      {error && <p role="alert" style={{ color: "var(--danger)", fontSize: 13 }}>{error}</p>}
      {reviewable && reviewerActive && advVerdict !== "approve" && (
        <p className="adv-banner working">
          🛡 Adversarial review in progress{advComments.length > 0 ? ` (round ${advComments.length + 1})` : ""} —
          another agent is checking {hasPR ? "this work" : "this spec"}. You'll only need to look once it approves.
        </p>
      )}
      {reviewable && !reviewerActive && advVerdict == null && (
        <p className="adv-banner pending">🛡 Awaiting adversarial review.</p>
      )}
      {advVerdict === "approve" && (
        <p className="adv-banner approved">🛡 Approved by adversarial review.</p>
      )}
      {advVerdict === "approve" && advComment && (
        <details className="adv-details">
          <summary>Reviewer's findings</summary>
          <div className="review-guide"><MdText text={String(advComment.data?.text ?? "")} /></div>
        </details>
      )}
      <ChecksRow checks={checks} />
      {guide ? (
        <div className="review-guide">
          <MdText text={String(guide.data?.text ?? "")} />
        </div>
      ) : (
        <p style={{ color: "var(--fog-dim)", fontSize: 13 }}>
          No review guide was posted — the trail and PR below are what there is.
        </p>
      )}
      {prs.map((l) => (
        <p key={l.url}><span className="chip">pr</span>{" "}
          <a href={l.url} target="_blank" rel="noreferrer">{l.label || l.url}</a></p>
      ))}
      <p>
        <Link className="btn ghost small" to={`/boards/${card.board_id}/cards/${card.number}/diff`}>
          View diff in blerg-board
        </Link>
      </p>
      {deployURL && (
        <p><a className="live-chip" href={deployURL} target="_blank" rel="noreferrer">● LIVE — verify there ↗</a></p>
      )}
      {!rejecting ? (
        <div style={{ marginTop: 10 }}>
        <p className="accept-means">
          {hasPR
            ? <>Accept will: merge the PR into main (deleting its branch), move the card to {doneCol?.name ?? "done"}, and stop the card's sessions. If the PR no longer merges cleanly, nothing merges — the card goes back to {workCol?.name ?? "in progress"} for a worker to resolve the conflict.</>
            : <>Accept will: mark the card accepted, move it to {doneCol?.name ?? "done"}, and stop the card's sessions. No PR is attached — nothing merges.</>}
          {" "}Reject sends your feedback to the working session and moves the card back to {workCol?.name ?? "in progress"}.
        </p>
        {me?.is_admin ? (
          <div className="row">
            <button className="btn small" onClick={accept}
              disabled={busy || awaitingAdversarial}
              title={awaitingAdversarial ? "waiting on the adversarial review verdict" : undefined}>
              {busy ? "Merging…" : hasPR ? "Accept & merge" : "Accept"}
            </button>
            <button className="btn ghost small" onClick={() => setRejecting(true)} disabled={busy}>
              Reject with feedback
            </button>
          </div>
        ) : (
          <p className="admin-only">Only a board admin can accept or reject this card.</p>
        )}
        </div>
      ) : (
        <div style={{ marginTop: 10 }}>
          <textarea
            rows={3} value={feedback} autoFocus
            placeholder="what needs to change…"
            onChange={(e) => setFeedback(e.target.value)}
          />
          <div className="row" style={{ marginTop: 6 }}>
            <button className="btn small" onClick={reject} disabled={busy || !feedback.trim()}>
              Send back
            </button>
            <button className="btn ghost small" onClick={() => setRejecting(false)}>Cancel</button>
          </div>
        </div>
      )}
    </section>
  );
}

// DocViewer: an in-app panel for a "doc" link — fetches the raw markdown
// server-side (avoids CORS, and blerg-board's fetch proxy checks the target isn't
// a private address) and renders it with MdText instead of navigating away.
function DocViewer({ cardId, link, onClose }: { cardId: string; link: CardLink; onClose: () => void }) {
  const [text, setText] = useState<string | null>(null);
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    fetch(`/api/cards/${cardId}/doc?url=${encodeURIComponent(link.url)}`, { credentials: "same-origin" })
      .then(async (resp) => {
        const body = await resp.text();
        if (cancelled) return;
        if (!resp.ok) {
          try { setError(JSON.parse(body).error ?? body); } catch { setError(body || `HTTP ${resp.status}`); }
          return;
        }
        setTruncated(resp.headers.get("X-Doc-Truncated") === "1");
        setText(body);
      })
      .catch((e) => { if (!cancelled) setError(String(e)); });
    return () => { cancelled = true; };
  }, [cardId, link.url]);

  return (
    <div className="doc-modal-backdrop" onClick={onClose}>
      <div className="doc-modal" onClick={(e) => e.stopPropagation()} role="dialog" aria-label={link.label || link.url}>
        <div className="doc-modal-head">
          <span className="chip doc">doc</span>
          <a className="doc-modal-title" href={link.url} target="_blank" rel="noreferrer">{link.label || link.url}</a>
          <span className="spacer" />
          <button className="btn ghost small" onClick={onClose}>Close</button>
        </div>
        <div className="doc-modal-body">
          {error && <p role="alert" style={{ color: "var(--danger)" }}>Couldn't load doc: {error}</p>}
          {!error && text == null && <div className="empty">fetching…</div>}
          {truncated && (
            <p style={{ color: "var(--fog-dim)", fontSize: 12 }}>truncated — showing the first part only</p>
          )}
          {text != null && <MdText text={text} />}
        </div>
      </div>
    </div>
  );
}

export function CardSheet({ card, board, columns, onClose }: {
  card: Card; board: Board; columns: Column[]; onClose: () => void;
}) {
  const [events, setEvents] = useState<CardEvent[]>([]);
  const [full, setFull] = useState<Card>(card);
  const isMirrored = !!board.driven_by;
  const { sessions } = useCardSessions(card.id);
  const hasSession = sessions.length > 0;
  // session panel collapse — a compact-view preference, remembered globally
  const [sessCollapsed, setSessCollapsed] = useState(
    () => sessionStorage.getItem("sess-collapsed") === "1");
  const setCollapsed = (v: boolean) => {
    setSessCollapsed(v);
    try { sessionStorage.setItem("sess-collapsed", v ? "1" : "0"); } catch { /* private mode */ }
  };
  const checks = useChecks(card.id, events.filter((e) => e.type === "comment"));
  // adversarial review context for the whole sheet: round count + latest verdict
  const advAll = events.filter((e) => e.type === "comment" &&
    String(e.data?.text ?? "").startsWith("Adversarial review") &&
    /verdict:\s*(approve|request[- ]changes)/i.test(String(e.data?.text ?? "")));
  const advLatest = advAll[advAll.length - 1];
  const advLatestVerdict = advLatest
    ? (/verdict:\s*approve/i.test(String(advLatest.data?.text ?? "")) ? "approve" : "request-changes")
    : null;
  const inWork = (full.column_id &&
    /progress|doing/.test((columns.find((c) => c.id === full.column_id)?.name ?? "").toLowerCase())) || false;
  const [docLink, setDocLink] = useState<CardLink | null>(null);
  // outcome of an action that moved the card without finishing it (Accept on
  // a conflicted PR) — the sheet stays open and says where the card went
  const [notice, setNotice] = useState("");
  const [autoMerge, setAutoMerge] = useState(card.auto_merge);
  const setAutoMergeFlag = async (v: boolean) => {
    setAutoMerge(v);
    await api(`/api/cards/${card.id}`, {
      method: "PATCH", json: { auto_merge: v },
    }).catch(() => setAutoMerge(!v));
  };

  const loadCard = useCallback(() => {
    api<Card>(`/api/cards/${card.id}`).then(setFull).catch(() => {});
    api<CardEvent[]>(`/api/cards/${card.id}/events`).then((e) => setEvents(e ?? [])).catch(() => {});
  }, [card.id]);
  useEffect(loadCard, [loadCard]);
  // live updates: sessions PATCH the card (PR links, comments, moves) while
  // the human watches — refresh on every board change ping
  useEffect(() => boardSocket(card.board_id, loadCard), [card.board_id, loadCard]);

  const colName = (id: string | null) =>
    id ? (columns.find((c) => c.id === id)?.name ?? "?") : "archived";
  const inReview = (full.column_id &&
    (columns.find((c) => c.id === full.column_id)?.name ?? "").toLowerCase().includes("review")) || false;

  const archive = async () => {
    await api(`/api/cards/${card.id}/archive`, { method: "POST" });
    onClose();
  };

  const schema: FieldDef[] = Array.isArray(board.field_schema) ? board.field_schema : [];
  const fieldEntries = schema
    .filter((d) => full.fields && full.fields[d.key] != null)
    .sort((a, b) => (a.order ?? 0) - (b.order ?? 0));

  return (
    <div className="sheet-backdrop" onClick={onClose}>
      <div
        className={`sheet${hasSession && !isMirrored && !sessCollapsed ? " with-session" : ""}`}
        onClick={(e) => e.stopPropagation()} role="dialog" aria-label={`Card #${card.number}`}
      >
        <div className="sheet-card">
          <div className="row">
            <span className="num mono">#{full.number}{full.external_id ? ` · ${full.external_id}` : ""}</span>
            <button className="chip board-here" onClick={onClose}
              title={`this card lives on the ${board.name} board — close the card to see it`}>
              {board.name}
            </button>
            <span className="spacer" />
            {hasSession && !isMirrored && (
              <Link className="btn ghost small session-link-mobile"
                to={`/boards/${board.id}/cards/${full.number}/session`}>
                Session →
              </Link>
            )}
            <button className="btn ghost small" onClick={onClose}>Close</button>
          </div>
          <h2>{full.title}</h2>
          <div className="row" style={{ gap: 6 }}>
            <span className="chip type">{full.type}</span>
            <span className="chip">{full.priority}</span>
            {full.size && <span className="chip">{full.size}</span>}
            {full.model && (
              <span className="chip mono"
                title="this card's sessions run on this model instead of the board's">
                {full.model}
              </span>
            )}
            <span className="chip">{colName(full.column_id)}</span>
            <TestChip status={checks.tests} />
            <LintChip lint={checks.lint} />
            <CoverageChip cov={checks.coverage} />
            {full.gate_flag && <span className={`dot ${full.gate_flag}`} title={full.gate_flag} />}
          </div>

          {!isMirrored && (
            <label className="automerge-toggle"
              title="when the adversarial review approves this card's PR, merge it and move to done without waiting for you">
              <input type="checkbox" checked={autoMerge}
                onChange={(e) => setAutoMergeFlag(e.target.checked)} />
              ⚡ Auto-merge on approval
            </label>
          )}

          {notice && (
            <p className="adv-banner working" role="status">⟳ {notice}</p>
          )}

          {inWork && !isMirrored && advLatestVerdict === "request-changes" && (
            <section className="review-box" style={{ borderColor: "var(--stone)" }}>
              <p className="adv-banner working" style={{ margin: 0 }}>
                ⟳ Revision round {advAll.length + 1} — the adversarial review requested
                changes and the authoring session is addressing the findings. It returns
                to review when done.
              </p>
              <details className="adv-details">
                <summary>What the reviewer asked for</summary>
                <div className="review-guide"><MdText text={String(advLatest?.data?.text ?? "")} /></div>
              </details>
            </section>
          )}

          {inReview && !isMirrored && (
            <ReviewSection card={full} events={events} columns={columns}
              deployURL={board.deploy_url} checks={checks}
              onDone={(n) => { loadCard(); if (n) setNotice(n); else onClose(); }} />
          )}

          {full.body && <div className="body-text"><MdText text={full.body} /></div>}

          {fieldEntries.length > 0 && (
            <section>
              <h4>Fields</h4>
              <dl className="kv">
                {fieldEntries.map((d) => (
                  <FieldRow key={d.key} def={d} value={full.fields[d.key]} />
                ))}
              </dl>
            </section>
          )}

          {(full.blockers?.length ?? 0) > 0 && (
            <section>
              <h4>Depends on</h4>
              {pendingBlockers(full).length > 0 && (
                <p className="dep-note">
                  ⛔ The dispatcher skips this card until every blocker below reaches a
                  done column. This is not <em>stuck</em> — nothing is wrong with the card
                  and no human action is needed, though you can drop the edge if it is
                  no longer real.
                </p>
              )}
              <div className="row">{full.blockers!.map((b) => (
                <span className={`chip dep${b.satisfied ? " done" : " blocked"}`} key={b.id}
                  title={b.satisfied ? "landed — no longer holding this card back" : "not in a done column yet"}>
                  {b.satisfied ? "✓" : "⛔"} #{b.number} {b.title}
                </span>
              ))}</div>
            </section>
          )}

          {(full.repos?.length ?? 0) > 0 && (
            <section>
              <h4>Repos</h4>
              <div className="row">{full.repos!.map((r, i) => (
                <span className="chip repo" key={r}>
                  {r}{i === 0 && <em className="primary-tag">primary</em>}
                </span>
              ))}</div>
            </section>
          )}

          {!isMirrored && <GitPanel cardId={full.id} boardId={board.id} cardNumber={full.number} />}

          {!isMirrored && <StatsPanel cardId={full.id} />}

          {(dedupedLinks(full.links).length > 0) && (
            <section>
              <h4>Links</h4>
              {dedupedLinks(full.links).map((l) => (
                <div key={l.kind + l.url}>
                  {l.kind === "doc" ? (
                    <button type="button" className="chip doc" onClick={() => setDocLink(l)}
                      title="open in blerg-board">doc</button>
                  ) : (
                    <span className="chip">{l.kind}</span>
                  )}{" "}
                  {l.kind === "doc" ? (
                    <button type="button" className="link-btn" onClick={() => setDocLink(l)}>
                      {l.label || l.url}
                    </button>
                  ) : (
                    <a href={l.url} target="_blank" rel="noreferrer">{l.label || l.url}</a>
                  )}
                </div>
              ))}
            </section>
          )}

          {docLink && <DocViewer key={`${full.id}:${docLink.url}`} cardId={full.id} link={docLink} onClose={() => setDocLink(null)} />}

          {!isMirrored && !hasSession && (
            <section>
              <h4>Session</h4>
              <Conversation cardId={full.id} />
            </section>
          )}

          {!isMirrored && (
            <section>
              <h4>Move</h4>
              <div className="row">
                {columns.filter((c) => c.id !== full.column_id).map((c) => (
                  <MoveButton key={c.id} card={full} column={c} onDone={onClose} />
                ))}
                {!full.archived_at && (
                  <button className="btn ghost small" onClick={archive}>Archive</button>
                )}
              </div>
            </section>
          )}
          {isMirrored && (
            <section>
              <h4>Lifecycle</h4>
              <p style={{ fontSize: 13, color: "var(--fog-dim)" }}>
                {board.driven_by} drives this card; blerg-board is the visibility surface. Act on it in {board.driven_by}.
              </p>
            </section>
          )}

          <section>
            <h4>Trail</h4>
            {events.slice().reverse().map((e) => (
              e.type === "comment" ? (
                <div className="event comment" key={e.id}>
                  <MdText text={String(e.data?.text ?? "")} />
                  <span className="who">{e.actor} · <TimeAgo iso={e.created_at} /></span>
                </div>
              ) : (
                <div className="event" key={e.id}>
                  <span>{e.type}{e.to_column_id ? ` → ${colName(e.to_column_id)}` : ""}</span>{" "}
                  <span className="who">{e.actor} · <TimeAgo iso={e.created_at} /></span>
                </div>
              )
            ))}
          </section>
        </div>

        {hasSession && !isMirrored && !sessCollapsed && (
          <div className="sheet-session">
            <div className="row" style={{ margin: "0 0 8px" }}>
              <h4 className="sign" style={{ fontSize: 12, color: "var(--fog-dim)", margin: 0 }}>Session</h4>
              <span className="spacer" />
              <button className="btn ghost small" onClick={() => setCollapsed(true)}
                title="collapse the session panel — the session keeps running">⟫</button>
            </div>
            <Conversation cardId={full.id} />
          </div>
        )}
        {hasSession && !isMirrored && sessCollapsed && (
          <button className="sess-tab" onClick={() => setCollapsed(false)} title="show the session panel">
            ⟪ session
          </button>
        )}
      </div>
    </div>
  );
}

function FieldRow({ def, value }: { def: FieldDef; value: unknown }) {
  return (
    <>
      <dt>{def.label || def.key}</dt>
      <dd><FieldValue def={def} value={value} /></dd>
    </>
  );
}

function MoveButton({ card, column, onDone }: { card: Card; column: Column; onDone: () => void }) {
  const move = async () => {
    await api(`/api/cards/${card.id}/move`, { json: { column_id: column.id } });
    onDone();
  };
  return <button className="btn ghost small" onClick={move}>→ {column.name}</button>;
}

// dedupedLinks collapses repeat links (same kind + label — e.g. one "runner
// session" per spawn attempt) down to the most recent occurrence.
function dedupedLinks(links: Card["links"]) {
  const seen = new Set<string>();
  const out: NonNullable<Card["links"]> = [];
  for (const l of (links ?? []).slice().reverse()) {
    const k = `${l.kind}|${l.label || l.url}`;
    if (seen.has(k)) continue;
    seen.add(k);
    out.unshift(l);
  }
  return out;
}
