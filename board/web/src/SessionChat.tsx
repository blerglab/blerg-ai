// SessionChat: one runner session shown by the shared chat (@blerglab/chat), the same surface the
// runner's own app uses — live text, tool groups, files, review and mark-up. The board brokers:
// the chat talks to the board's /api/chat proxy with the board's own bearer token, and the board
// passes each request to the runner for that one session after its own permission check.
import { useMemo } from "react";
import { ChatView, createProxyTransport, type DescribeUserMessage, type SessionMeta, type Transport } from "@blerglab/chat";
import "@blerglab/chat/blerg.css";
import { ensureFreshToken, getAccessToken } from "./authClient";
import { bounceToRefresh } from "./signIn";
import { SessionModelControls } from "./SessionModelControls";

/** The session as the board's own API lists it. */
export interface RunnerSession {
  id: string;
  runner: string;
  external_session_id: string;
  lifecycle: string;
  role: string; // "worker" | "reviewer" | "discuss" | "board"
  resumable: boolean;
  created_at: string;
  model?: string;
  steering_supported?: boolean;
  engine?: string;
  effort?: string;
  board_id?: string;
  resume_mode?: string;
  resume_reason?: string;
  session_url?: string;
}

// A cheap, UNVERIFIED read of the token's exp claim: only to decide whether to renew before use.
function expired(token: string): boolean {
  try {
    const payload = JSON.parse(atob(token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/"))) as { exp?: unknown };
    return typeof payload.exp === "number" && payload.exp * 1000 <= Date.now() + 5_000;
  } catch {
    return false;
  }
}

// One transport for the page. The token is read fresh for every request, and renewed first when
// it has run out: the live stream reconnects by itself, and a reconnect with a dead token would
// be refused for good. A 401 that renewal cannot fix sends the page to core, as api() does.
let shared: Transport | null = null;
function boardTransport(): Transport {
  if (shared) return shared;
  shared = createProxyTransport({
    baseUrl: "/api/chat",
    headers: async () => {
      let token = getAccessToken();
      if (!token || expired(token)) {
        await ensureFreshToken();
        token = getAccessToken();
      }
      return token ? { Authorization: `Bearer ${token}` } : ({} as Record<string, string>);
    },
    onUnauthorized: () => {
      void ensureFreshToken().then((ok) => {
        if (ok) return;
        bounceToRefresh();
      });
    },
  });
  return shared;
}

// Who a user message is from. The prompt the board started the session with (its first message,
// recorded as "blerg-board", or as "chat" by a runner that predates that) is its brief, not
// conversation; any later "chat" message is a person's, typed in the runner's own app. What a
// signed-in person typed here, an answer to the agent's question and a reply to its note are
// theirs; a parent session's or a cron's message keeps the chat's own author for it; everything
// else the board sent on its own — a review's findings relayed to the worker, a dispatcher's
// nudge — and it says so. Text nobody typed (the restart note, a command's output) never reaches
// this: the chat draws it as a system card.
export const describeUserMessage: DescribeUserMessage = (_ev, p, ctx) => {
  if (p.source === "chat") return ctx.first ? { brief: true } : null;
  if (p.source === "blerg-board" && ctx.first) return { brief: true };
  if (p.source === "human" || p.source === "ask_answer" || p.source === "note_reply") return null;
  if (p.source === "session" || p.source === "cron") return null;
  return { author: "blerg-board" };
};

/** The runner's session status as the chat gets it, completed from the board's own row: when the
 *  board started it and on which model. Everything else, the runner's advertised file `limits`
 *  among it (the chat refuses an attachment over `limits.max_file_bytes` before sending it), is
 *  the runner's. */
export function boardMeta(m: SessionMeta, row: Pick<RunnerSession, "created_at" | "model" | "effort" | "engine" | "steering_supported">): SessionMeta {
  return { ...m, started_at: m.started_at || row.created_at, model: m.model ?? (row.model || undefined), effort: m.effort ?? row.effort, engine: m.engine ?? row.engine, steering_supported: m.steering_supported ?? row.steering_supported };
}

export function SessionChat({ session, placeholder, readOnly }: {
  session: RunnerSession;
  placeholder?: string;
  /** No composer: a session the board will not let the person message. */
  readOnly?: boolean;
}) {
  const { created_at, model, effort, engine, steering_supported, session_url, id: sessionId } = session;
  // The runner's status says how the session is, not when the board started it or on which
  // model; the board's own row does.
  const transport = useMemo<Transport>(() => {
    const base = boardTransport();
    return {
      ...base,
      publishedSessionUrl: (id) => id === sessionId ? session_url ?? null : null,
      session: async (id) => boardMeta(await base.session(id), { created_at, model, effort, engine, steering_supported }),
    };
  }, [created_at, model, effort, engine, steering_supported, session_url, sessionId]);
  return (
    <div className="session-chat">
      <ChatView
        // Keyed by session: a draft, a queued message or an open file belongs to one session.
        key={session.id}
        session={session.id}
        transport={transport}
        describeUserMessage={describeUserMessage}
        slots={{ header: readOnly ? undefined : <SessionModelControls session={session} /> }}
        placeholder={placeholder}
        readOnly={readOnly}
      />
    </div>
  );
}
