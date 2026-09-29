import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { Composer } from "./Composer";
import { ChatEvent, lastSpokenKind, latestModel, PendingMsg, RunnerEvent, RunnerSession, StartingHint, transcript, TypingDots } from "./Conversation";
import { useStickToBottom } from "./useStickToBottom";

// BoardChat: the ephemeral board-level session — a drawer over the board with
// a chat and an explicit close (which ends the session, not just the panel).
export function BoardChat({ boardId, onClosed }: { boardId: string; onClosed: () => void }) {
  const [minimized, setMinimized] = useState(() => sessionStorage.getItem(`chatmin:${boardId}`) === "1");
  const setMin = useCallback((v: boolean) => {
    setMinimized(v);
    try { sessionStorage.setItem(`chatmin:${boardId}`, v ? "1" : "0"); } catch { /* private mode */ }
  }, [boardId]);
  const [session, setSession] = useState<RunnerSession | null>(null);
  const [events, setEvents] = useState<RunnerEvent[]>([]);
  const [text, setText] = useState("");
  const [sent, setSent] = useState<string[]>([]);
  // a sent message is pending until the stream echoes it back — derived from
  // the events rather than pruned by an effect
  const pending = sent.filter((t) =>
    !events.some((e) => e.kind === "user_turn" && String(e.payload?.text ?? "") === t));
  const [error, setError] = useState("");
  const rootRef = useRef<HTMLDivElement>(null);
  // derived here rather than below the effects so auto-follow can key on the
  // list the transcript actually draws — a status event grows `events` without
  // adding a bubble, and re-pinning for it would be a no-op at best
  const { shown, spoken } = transcript(events);
  // this also covers minimize/restore, which used to need its own effect:
  // minimizing returns a bare button in place of the drawer, so React unmounts
  // `.convo` outright, and the hook pins any freshly mounted node to the latest
  // message — a new node has no reader position left to protect
  const { ref: convoRef, onScroll: onConvoScroll, scrollToBottom } = useStickToBottom([shown.length, pending.length], { resetKey: session?.id });

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

  const sessionId = session?.id;
  useEffect(() => {
    if (!sessionId) return;
    let stop = false;
    const tick = async () => {
      if (stop) return;
      try {
        const evs = await api<RunnerEvent[]>(`/api/runner-sessions/${sessionId}/events`);
        setEvents(evs ?? []);
        // keep lifecycle fresh — it drives the typing dots and send gating
        const live = await api<RunnerSession[]>(`/api/boards/${boardId}/sessions`);
        const mine = live?.find((x) => x.id === sessionId);
        setSession((cur) => cur && (mine ?? { ...cur, lifecycle: "stopped" }));
      } catch { /* transient */ }
    };
    tick();
    const iv = setInterval(tick, 3000);
    return () => { stop = true; clearInterval(iv); };
  }, [sessionId, boardId]);

  // a disconnected board session resumes with amnesia (the CLI transcript
  // lives in the dead pod) — for a chat that's useless, so treat it as ended
  const connected = session != null &&
    !["stopped", "error", "disconnected"].includes(session.lifecycle);
  const model = latestModel(events);

  const send = async () => {
    if (!text.trim() || !session) return;
    const t = text;
    setText("");
    setSent((p) => [...p, t]);
    scrollToBottom(); // sending re-arms auto-follow wherever you were reading
    try {
      await api(`/api/runner-sessions/${session.id}/message`, { json: { text: t } });
    } catch (err) {
      setError(err instanceof Error ? err.message : "send failed");
      setSent((p) => p.filter((x) => x !== t));
      setText(t);
    }
  };

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
        {model && (
          <span className="mono" style={{ color: "var(--fog-dim)", fontSize: 11 }}>{model}</span>
        )}
        <span className="spacer" />
        <button className="btn ghost small" onClick={() => setMin(true)}
          title="minimize — the session keeps running">—</button>
        <button className="btn ghost small" onClick={close} title="ends the session">✕ Close</button>
      </div>
      {error && <p role="alert" style={{ color: "var(--danger)", fontSize: 13, padding: "0 12px" }}>{error}</p>}
      <div className="convo board-chat-convo" ref={convoRef} onScroll={onConvoScroll}>
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
        {connected && spoken.length === 0 && <StartingHint since={session.created_at} />}
        {shown.map((e) => <ChatEvent e={e} key={e.seq} />)}
        {pending.map((t, i) => <PendingMsg text={t} key={`p${i}`} />)}
        {pending.length === 0 && spoken.length > 0 && connected &&
          (session.lifecycle === "running" || session.lifecycle === "starting" ||
            lastSpokenKind(spoken) === "user_turn") && <TypingDots />}
      </div>
      {connected && (
        <div className="board-chat-compose">
          <Composer value={text} onChange={setText} onSend={send}
            disabled={false}
            placeholder="ask, triage, file cards… (Shift+Enter for newline)" />
        </div>
      )}
    </div>
  );
}
