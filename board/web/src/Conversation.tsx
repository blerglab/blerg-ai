import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { Composer } from "./Composer";
import { CodeBlock } from "./CodeBlock";
import { useStickToBottom } from "./useStickToBottom";

export interface RunnerSession {
  id: string;
  runner: string;
  external_session_id: string;
  lifecycle: string;
  role: string; // "worker" | "reviewer"
  resumable: boolean;
  created_at: string;
}

export interface RunnerEvent {
  seq: number;
  ts: string;
  kind: string;
  payload: Record<string, unknown>;
}

// ── md-lite: enough formatting for agent prose, no dependency ────────────────
// Handles: fenced ``` code blocks, `inline code`, **bold**, bare links,
// paragraph normalization (trim + collapse 3+ newlines).

function inlineMd(text: string): React.ReactNode[] {
  const out: React.ReactNode[] = [];
  // Split on **bold**, `code`, and URLs, keeping delimiters.
  const re = /(\*\*[^*]+\*\*|`[^`]+`|https?:\/\/[^\s)]+)/g;
  let last = 0, m: RegExpExecArray | null, k = 0;
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const tok = m[0];
    if (tok.startsWith("**")) out.push(<strong key={k++}>{tok.slice(2, -2)}</strong>);
    else if (tok.startsWith("`")) out.push(<code key={k++}>{tok.slice(1, -1)}</code>);
    else out.push(<a key={k++} href={tok} target="_blank" rel="noreferrer">{tok}</a>);
    last = m.index + tok.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

export function MdText({ text }: { text: string }) {
  const norm = text.trim().replace(/\r/g, "");
  const out: React.ReactNode[] = [];
  const re = /```(\w*)[ \t]*\n?([\s\S]*?)(?:```|$)/g;
  let last = 0, m: RegExpExecArray | null, k = 0;
  while ((m = re.exec(norm)) !== null) {
    if (m.index > last) out.push(<Blocks text={norm.slice(last, m.index)} key={k++} />);
    out.push(<CodeBlock code={m[2].replace(/\n$/, "")} lang={m[1]} key={k++} />);
    last = m.index + m[0].length;
  }
  if (last < norm.length) out.push(<Blocks text={norm.slice(last)} key={k++} />);
  return <>{out}</>;
}

// Blocks: a tiny block parser — paragraphs, -/* bullet lists, 1. numbered
// lists, and --- rules. Explicit <br/> for single newlines inside a
// paragraph, real <ul>/<ol> for lists.
function Blocks({ text }: { text: string }) {
  const lines = text.split("\n");
  const out: React.ReactNode[] = [];
  let i = 0, key = 0;
  const isBullet = (l: string) => /^\s*[-*]\s+/.test(l);
  const isNumbered = (l: string) => /^\s*\d+[.)]\s+/.test(l);
  while (i < lines.length) {
    if (!lines[i].trim()) { i++; continue; }
    if (/^\s*(-{3,}|\*{3,})\s*$/.test(lines[i])) {
      out.push(<hr key={key++} />); i++; continue;
    }
    const h = lines[i].match(/^\s*(#{1,4})\s+(.*)/);
    if (h) {
      out.push(<p className="md-h" key={key++}>{inlineMd(h[2])}</p>);
      i++; continue;
    }
    if (isBullet(lines[i]) || isNumbered(lines[i])) {
      const numbered = isNumbered(lines[i]);
      const start = numbered ? Number(lines[i].match(/^\s*(\d+)/)?.[1] ?? 1) : 1;
      const items: string[] = [];
      while (i < lines.length && (isBullet(lines[i]) || isNumbered(lines[i]))) {
        items.push(lines[i].replace(/^\s*(?:[-*]|\d+[.)])\s+/, ""));
        i++;
        // continuation lines (indented) belong to the previous item
        while (i < lines.length && lines[i].trim() && !isBullet(lines[i]) && !isNumbered(lines[i]) && /^\s{2,}/.test(lines[i])) {
          items[items.length - 1] += " " + lines[i].trim();
          i++;
        }
      }
      out.push(
        numbered ? (
          <ol key={key++} start={start}>
            {items.map((it, j) => <li key={j}>{inlineMd(it)}</li>)}
          </ol>
        ) : (
          <ul key={key++}>
            {items.map((it, j) => <li key={j}>{inlineMd(it)}</li>)}
          </ul>
        ),
      );
      continue;
    }
    // paragraph: consecutive non-blank, non-list lines
    const para: string[] = [];
    while (i < lines.length && lines[i].trim() && !isBullet(lines[i]) && !isNumbered(lines[i]) && !/^\s*(-{3,}|\*{3,})\s*$/.test(lines[i])) {
      para.push(lines[i].trim());
      i++;
    }
    // markdown soft-wrap: single newlines inside a paragraph join with a space
    out.push(<p key={key++}>{inlineMd(para.join(" "))}</p>);
  }
  return <>{out}</>;
}

// isKickoff: the runner-generated role brief handed to the session at spawn.
// The runner tags it `source: "chat"`, which separates it cleanly from the two
// other kinds of user_turn — `human` (composer input) and `blerg-board` (mid-session
// automation relays, e.g. "the worker pushed an update addressing your
// findings"). Those relays explain why the agent suddenly acts and MUST stay
// visible; only the brief is metadata.
export function isKickoff(e: RunnerEvent): boolean {
  return e.kind === "user_turn" && String(e.payload?.source ?? "") === "chat";
}

// kinds ChatEvent actually draws; everything else is plumbing the state chip
// already covers.
const RENDERED_KINDS = ["user_turn", "assistant_turn", "tool_call", "tool_result", "error"];

// transcript: the two lists a chat view needs, derived once from the raw
// stream.
//
//   shown  — what the transcript draws, in order, the brief included as its
//            collapsed line.
//   spoken — what counts as conversation: `shown` minus the brief.
//
// Every "has anything happened yet?" question — StartingHint, the typing dots,
// lastSpokenKind — must be asked of `spoken`, never of the raw stream. The
// brief lands within seconds of spawn, well before the agent says anything, so
// counting it collapses the loading state for the whole cold start.
export function transcript(events: RunnerEvent[]): { shown: RunnerEvent[]; spoken: RunnerEvent[] } {
  const shown = events.filter((e) => RENDERED_KINDS.includes(e.kind));
  return { shown, spoken: shown.filter((e) => !isKickoff(e)) };
}

// speakerFor: honest attribution. Kickoff prompts and automation come from
// blerg-board; only composer input from a logged-in human is "you".
function speakerFor(p: Record<string, unknown>): { label: string; cls: string } {
  const source = String(p.source ?? "");
  if (source === "human" || source === "ask_answer") return { label: "you", cls: "you" };
  return { label: "blerg-board", cls: "blerg-board" };
}

// toolSummary compresses a tool_call payload to "Bash · make test" style.
function toolSummary(p: Record<string, unknown>): string {
  const tool = String(p.tool ?? p.name ?? "tool");
  const input = (p.input ?? {}) as Record<string, unknown>;
  const detail =
    input.command ?? input.file_path ?? input.path ?? input.pattern ??
    input.query ?? input.url ?? "";
  return detail ? `${tool} · ${String(detail).slice(0, 90)}` : tool;
}

function resultSummary(p: Record<string, unknown>): string {
  let out = String(p.output ?? p.content ?? "");
  if (out.startsWith('"')) {
    try { const v = JSON.parse(out); if (typeof v === "string") out = v; } catch { /* raw */ }
  }
  const line = out.split("\n").find((l) => l.trim()) ?? "";
  return (p.is_error ? "✗ " : "") + line.slice(0, 100);
}

// KickoffLine: the role brief the runner handed the session at spawn, as one
// collapsed line — session metadata, not conversation, so it wears the tool
// line's dim monospace rather than a message bubble. Collapsed on every mount
// (including after the board drawer is minimized and restored); expanding is a
// pure reveal that changes no counts and reorders nothing.
function KickoffLine({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="kickoff">
      <button className="toolline kickoff-toggle" onClick={() => setOpen(!open)}
        aria-expanded={open} title="the prompt this session was started with">
        {open ? "▾" : "▸"} session brief · {text.length.toLocaleString()} chars
      </button>
      {open && <div className="kickoff-body"><MdText text={text} /></div>}
    </div>
  );
}

// ChatEvent renders one event in chat form. Status noise is dropped —
// lifecycle lives in the state chip.
export function ChatEvent({ e }: { e: RunnerEvent }) {
  const p = e.payload ?? {};
  switch (e.kind) {
    case "user_turn": {
      if (isKickoff(e)) return <KickoffLine text={String(p.text ?? "")} />;
      const who = speakerFor(p);
      return (
        <div className={`msg ${who.cls}`}>
          <span className="speaker">{who.label}</span>
          <MdText text={String(p.text ?? "")} />
        </div>
      );
    }
    case "assistant_turn":
      return (
        <div className="msg agent">
          <span className="speaker">agent</span>
          <MdText text={String(p.text ?? p.message ?? "")} />
        </div>
      );
    case "tool_call":
      return <div className="toolline">▸ {toolSummary(p)}</div>;
    case "tool_result":
      return <div className="toolline dim">← {resultSummary(p)}</div>;
    case "error":
      return (
        <div className="msg err">
          <span className="speaker">error</span>
          <MdText text={String(p.message ?? "")} />
        </div>
      );
    default:
      return null; // status/plumbing — the chip shows lifecycle
  }
}

// latestModel: the model the session most recently ran, from a turn_done
// status event (kind "status", payload.model — plumbing kinds without a
// dedicated mapping get wrapped in source_kind/payload, so this only ever
// matches the flat turn_done shape).
export function latestModel(events: RunnerEvent[]): string | null {
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i];
    if (e.kind === "status" && typeof e.payload?.model === "string") return e.payload.model as string;
  }
  return null;
}

// lastSpokenKind: the newest event that actually renders as conversation —
// decides whether the agent still "owes" a reply (→ typing dots). Callers pass
// transcript().spoken; re-deriving here keeps it honest (and idempotent) if a
// raw stream is ever passed instead.
export function lastSpokenKind(events: RunnerEvent[]): string | null {
  const { spoken } = transcript(events);
  return spoken.length ? spoken[spoken.length - 1].kind : null;
}

// PendingMsg: the just-sent message, shown immediately with a send spinner
// until it appears in the polled event stream.
export function PendingMsg({ text }: { text: string }) {
  return (
    <div className="msg you pending">
      <span className="speaker">you</span>
      <MdText text={text} />
      <span className="sendspin" aria-label="sending" />
    </div>
  );
}

// StartingHint: what "waiting" actually means, with elapsed time so a slow
// cold start reads as progress, not a hang.
export function StartingHint({ since }: { since: string }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const iv = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(iv);
  }, []);
  const secs = Math.max(0, Math.floor((now - new Date(since).getTime()) / 1000));
  return (
    <div className="empty starting-hint">
      <TypingDots />
      <p>
        Session starting — container, repo clone, then the agent reads the
        board before speaking. Usually under a minute; cold nodes take
        longer. <span className="mono">{secs}s</span>
      </p>
    </div>
  );
}

// TypingDots: the agent is working on a reply.
export function TypingDots() {
  return (
    <div className="typing" aria-label="agent is responding">
      <span /><span /><span />
    </div>
  );
}

export function useCardSessions(cardId: string) {
  const [sessions, setSessions] = useState<RunnerSession[]>([]);
  const load = useCallback(() =>
    api<RunnerSession[]>(`/api/cards/${cardId}/runner-sessions`)
      .then((s) => setSessions(s ?? []))
      .catch(() => {}), [cardId]);
  useEffect(() => {
    load();
    // any instance's spawn notifies every other instance (the sheet's
    // layout split depends on a different hook instance than the spawner)
    const onChange = () => load();
    window.addEventListener("blerg-board:sessions-changed", onChange);
    return () => window.removeEventListener("blerg-board:sessions-changed", onChange);
  }, [load]);
  const workers = sessions.filter((s) => s.role !== "reviewer");
  const reviewers = sessions.filter((s) => s.role === "reviewer");
  return {
    sessions, reload: load,
    // the conversation panel follows the worker; reviewers get a status line
    current: workers[workers.length - 1] ?? sessions[sessions.length - 1],
    reviewer: reviewers[reviewers.length - 1],
  };
}

// Conversation: the session panel body — a parsed chat over the event
// stream, with composer + interrupt. Rendered natively; the stream is the
// contract (no iframe, no PTY).
export function Conversation({ cardId }: { cardId: string }) {
  const { sessions, reload, current } = useCardSessions(cardId);
  const [events, setEvents] = useState<RunnerEvent[]>([]);
  const [text, setText] = useState("");
  const [sent, setSent] = useState<string[]>([]);
  // a sent message is pending until the stream echoes it back — derived from
  // the events rather than pruned by an effect
  const pending = sent.filter((t) =>
    !events.some((e) => e.kind === "user_turn" && String(e.payload?.text ?? "") === t));
  const [spawning, setSpawning] = useState(false);
  const [error, setError] = useState("");
  const panelRef = useRef<HTMLDivElement>(null);
  // derived here rather than below so auto-follow keys on the drawn transcript,
  // not the raw stream — a status event grows `events` without adding a bubble
  const { shown, spoken } = transcript(events);
  // resetKey: "Run again" swaps the transcript under the same DOM node, and the
  // new session starts at its own bottom no matter where you left the old one
  const { ref: convoRef, onScroll: onConvoScroll, scrollToBottom } = useStickToBottom([shown.length, pending.length], { resetKey: current?.id });

  // fleet-table clicks (and future hotkeys) land focus in this composer
  useEffect(() => {
    const focus = () => panelRef.current?.querySelector("textarea")?.focus();
    window.addEventListener("blerg-board:card-chat-focus", focus);
    return () => window.removeEventListener("blerg-board:card-chat-focus", focus);
  }, []);

  const currentId = current?.id;
  useEffect(() => {
    if (!currentId) return;
    let stop = false;
    const tick = async () => {
      if (stop) return;
      try {
        const evs = await api<RunnerEvent[]>(`/api/runner-sessions/${currentId}/events`);
        setEvents(evs ?? []);
      } catch { /* transient */ }
    };
    tick();
    const iv = setInterval(() => { tick(); reload(); }, 3000);
    return () => { stop = true; clearInterval(iv); };
  }, [currentId, reload]);

  const spawn = async (mode: "run" | "discuss" = "run") => {
    setSpawning(true); setError("");
    try {
      await api(`/api/cards/${cardId}/spawn`, { json: { mode } });
      await reload();
      window.dispatchEvent(new Event("blerg-board:sessions-changed"));
    } catch (e) {
      setError(e instanceof Error ? e.message : "spawn failed");
    } finally {
      setSpawning(false);
    }
  };

  const connected = current != null &&
    !["stopped", "error"].includes(current.lifecycle);
  const model = latestModel(events);

  const send = async () => {
    if (!text.trim() || !current) return;
    const t = text;
    setText("");
    setSent((p) => [...p, t]);
    scrollToBottom(); // sending re-arms auto-follow wherever you were reading
    try {
      await api(`/api/runner-sessions/${current.id}/message`, { json: { text: t } });
    } catch (err) {
      setError(err instanceof Error ? err.message : "send failed");
      setSent((p) => p.filter((x) => x !== t));
      setText(t);
    }
  };

  const interrupt = () =>
    current && api(`/api/runner-sessions/${current.id}/interrupt`, { method: "POST" }).catch(() => {});

  if (!current) {
    return (
      <div>
        {error && <p role="alert" style={{ color: "var(--danger)", fontSize: 13 }}>{error}</p>}
        <div className="row" style={{ gap: 8 }}>
          <button className="btn small" onClick={() => spawn("run")} disabled={spawning}>
            {spawning ? "Starting…" : "▶ Run this card"}
          </button>
          <button className="btn ghost small" onClick={() => spawn("discuss")} disabled={spawning}>
            💬 Discuss first
          </button>
        </div>
        <p className="spawn-hint">
          Run moves the card to in&nbsp;progress and an agent executes it end to end.
          Discuss starts a read-only conversation about the card — nothing is
          changed or moved until you say run.
        </p>
      </div>
    );
  }

  return (
    <div className="convo-panel" ref={panelRef}>
      {error && <p role="alert" style={{ color: "var(--danger)", fontSize: 13 }}>{error}</p>}
      <div className="row convo-head">
        <span className={`convo-state ${current.lifecycle}`}>
          {current.lifecycle === "waiting" ? "? waiting on you" : current.lifecycle}
        </span>
        <span className="mono" style={{ color: "var(--fog-dim)", fontSize: 11 }}>
          {current.role === "discuss" ? "discussion · " : ""}{current.runner} · {current.external_session_id.slice(0, 8)}
          {model ? ` · ${model}` : ""}
          {sessions.length > 1 ? ` · attempt ${sessions.length}` : ""}
        </span>
        <span className="spacer" />
        {current.role === "discuss" && (
          <button className="btn small" onClick={() => spawn("run")} disabled={spawning}
            title="move the card to in progress and start an executing session">
            ▶ Run this card
          </button>
        )}
        {(current.lifecycle === "running" || current.lifecycle === "waiting") && (
          <button className="btn ghost small" onClick={interrupt}>Interrupt</button>
        )}
        {(current.lifecycle === "stopped" || current.lifecycle === "error") && (
          <button className="btn ghost small" onClick={() => spawn("run")} disabled={spawning}>Run again</button>
        )}
      </div>
      <div className="convo" ref={convoRef} onScroll={onConvoScroll}>
        {spoken.length === 0 && connected && <StartingHint since={current.created_at} />}
        {shown.map((e) => <ChatEvent e={e} key={e.seq} />)}
        {pending.map((t, i) => <PendingMsg text={t} key={`p${i}`} />)}
        {pending.length === 0 && spoken.length > 0 && connected &&
          (current.lifecycle === "running" || current.lifecycle === "starting" ||
            lastSpokenKind(spoken) === "user_turn") && <TypingDots />}
      </div>
      {connected ? (
        <div style={{ marginTop: 8 }}>
          <Composer value={text} onChange={setText} onSend={send}
            disabled={false}
            placeholder="steer the session… (Shift+Enter for newline)" />
        </div>
      ) : (
        <p className="session-ended-hint">
          Session ended — the transcript above is the record.
          {" "}Use “Run again” to start a fresh session on this card.
        </p>
      )}
    </div>
  );
}
