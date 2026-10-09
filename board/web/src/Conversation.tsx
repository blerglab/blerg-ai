import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { CodeBlock } from "./CodeBlock";
import { SessionChat, type RunnerSession } from "./SessionChat";

export type { RunnerSession } from "./SessionChat";

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

// Conversation: the session panel of a card. The board's part is which session
// it is and what can be done about the card — run it, discuss it, run it again;
// the conversation itself is the shared chat (SessionChat), live from the runner
// through the board's proxy.
export function Conversation({ cardId }: { cardId: string }) {
  const { sessions, reload, current } = useCardSessions(cardId);
  const [spawning, setSpawning] = useState(false);
  const [error, setError] = useState("");
  const panelRef = useRef<HTMLDivElement>(null);

  // fleet-table clicks (and future hotkeys) land focus in this composer
  useEffect(() => {
    const focus = () => panelRef.current?.querySelector("textarea")?.focus();
    window.addEventListener("blerg-board:card-chat-focus", focus);
    return () => window.removeEventListener("blerg-board:card-chat-focus", focus);
  }, []);

  // The chat follows the session by itself; the board's own row (which session
  // is current, its lifecycle chip, whether a rerun is on offer) is polled.
  const currentId = current?.id;
  useEffect(() => {
    if (!currentId) return;
    const iv = setInterval(() => { void reload(); }, 3000);
    return () => clearInterval(iv);
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

  const over = current.lifecycle === "stopped" || current.lifecycle === "error";
  return (
    <div className="convo-panel" ref={panelRef}>
      {error && <p role="alert" style={{ color: "var(--danger)", fontSize: 13 }}>{error}</p>}
      <div className="row convo-head">
        <span className={`convo-state ${current.lifecycle}`}>
          {current.lifecycle === "waiting" ? "? waiting on you" : current.lifecycle}
        </span>
        <span className="mono" style={{ color: "var(--fog-dim)", fontSize: 11 }}>
          {current.role === "discuss" ? "discussion · " : ""}{current.runner} · {current.external_session_id.slice(0, 8)}
          {current.model ? ` · ${current.model}` : ""}
          {sessions.length > 1 ? ` · attempt ${sessions.length}` : ""}
        </span>
        <span className="spacer" />
        {current.role === "discuss" && (
          <button className="btn small" onClick={() => spawn("run")} disabled={spawning}
            title="move the card to in progress and start an executing session">
            ▶ Run this card
          </button>
        )}
        {over && (
          <button className="btn ghost small" onClick={() => spawn("run")} disabled={spawning}>Run again</button>
        )}
      </div>
      <SessionChat session={current} placeholder="steer the session… (Shift+Enter for newline)" />
    </div>
  );
}
