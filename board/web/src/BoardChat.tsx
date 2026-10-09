import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { SessionChat, type RunnerSession } from "./SessionChat";

// BoardChat: the ephemeral board-level session — a drawer over the board with
// a chat and an explicit close (which ends the session, not just the panel).
// The drawer, its lifecycle chip and its close are the board's; the chat inside
// is the shared one (SessionChat).
export function BoardChat({ boardId, onClosed }: { boardId: string; onClosed: () => void }) {
  const [minimized, setMinimized] = useState(() => sessionStorage.getItem(`chatmin:${boardId}`) === "1");
  const setMin = useCallback((v: boolean) => {
    setMinimized(v);
    try { sessionStorage.setItem(`chatmin:${boardId}`, v ? "1" : "0"); } catch { /* private mode */ }
  }, [boardId]);
  const [session, setSession] = useState<RunnerSession | null>(null);
  const [error, setError] = useState("");
  const rootRef = useRef<HTMLDivElement>(null);

  // focus the composer on open and whenever the "c" hotkey re-fires
  useEffect(() => {
    const focusComposer = () => {
      setMin(false); // the c hotkey restores a minimized drawer
      setTimeout(() => rootRef.current?.querySelector("textarea")?.focus(), 60);
    };
    window.addEventListener("blerg-board:chat-focus", focusComposer);
    return () => window.removeEventListener("blerg-board:chat-focus", focusComposer);
  }, [setMin]);

  // reattach to a live board session, or start one
  useEffect(() => {
    let stop = false;
    (async () => {
      try {
        const existing = await api<RunnerSession[]>(`/api/boards/${boardId}/sessions`);
        if (stop) return;
        if (existing?.length) { setSession(existing[0]); return; }
        await api(`/api/boards/${boardId}/sessions`, { json: {} });
        const created = await api<RunnerSession[]>(`/api/boards/${boardId}/sessions`);
        if (!stop) setSession(created?.[0] ?? null);
      } catch (e) {
        if (!stop) setError(e instanceof Error ? e.message : "failed to start");
      }
    })();
    return () => { stop = true; };
  }, [boardId]);

  // keep the board's own row fresh: it drives the chip, the mini dot and whether
  // the person may still write
  const sessionId = session?.id;
  useEffect(() => {
    if (!sessionId) return;
    let stop = false;
    const tick = async () => {
      try {
        const live = await api<RunnerSession[]>(`/api/boards/${boardId}/sessions`);
        if (stop) return;
        const mine = live?.find((x) => x.id === sessionId);
        setSession((cur) => cur && (mine ?? { ...cur, lifecycle: "stopped" }));
      } catch { /* transient */ }
    };
    const iv = setInterval(tick, 3000);
    return () => { stop = true; clearInterval(iv); };
  }, [sessionId, boardId]);

  // a disconnected board session resumes with amnesia (the CLI transcript
  // lives in the dead pod) — for a chat that's useless, so treat it as ended
  const connected = session != null &&
    !["stopped", "error", "disconnected"].includes(session.lifecycle);

  const close = async () => {
    if (session) await api(`/api/runner-sessions/${session.id}/close`, { method: "POST" }).catch(() => {});
    onClosed();
  };

  if (minimized) {
    const lc = session?.lifecycle ?? "starting";
    return (
      <button className="board-chat-mini" onClick={() => { setMin(false); setTimeout(() => rootRef.current?.querySelector("textarea")?.focus(), 80); }}
        title={`board session (${lc}) — click to restore`}>
        💬 <span className={`mini-dot ${lc}`} />
      </button>
    );
  }

  return (
    <div className="board-chat" role="dialog" aria-label="Board session" ref={rootRef}>
      <div className="row board-chat-head">
        <span className="sign">Board session</span>
        {session && <span className={`convo-state ${session.lifecycle}`}>{session.lifecycle}</span>}
        {session?.model && (
          <span className="mono" style={{ color: "var(--fog-dim)", fontSize: 11 }}>{session.model}</span>
        )}
        <span className="spacer" />
        <button className="btn ghost small" onClick={() => setMin(true)}
          title="minimize — the session keeps running">—</button>
        <button className="btn ghost small" onClick={close} title="ends the session">✕ Close</button>
      </div>
      {error && <p role="alert" style={{ color: "var(--danger)", fontSize: 13, padding: "0 12px" }}>{error}</p>}
      {!session && !error && <div className="empty" style={{ padding: 12, fontSize: 13 }}>starting…</div>}
      {session?.lifecycle === "disconnected" && (
        <p className="adv-banner working" style={{ margin: 8 }}>
          This session's pod is gone — its conversation can't continue.
          Close and reopen to start a fresh board session.
        </p>
      )}
      {session?.lifecycle === "error" && (
        <p className="adv-banner working" style={{ margin: 8 }}>
          The session failed to start — most often the board's repo doesn't
          exist or isn't reachable. Check the board's repos, then close and
          reopen.
        </p>
      )}
      {session && (
        <SessionChat session={session} readOnly={!connected}
          placeholder="ask, triage, file cards… (Shift+Enter for newline)" />
      )}
    </div>
  );
}
