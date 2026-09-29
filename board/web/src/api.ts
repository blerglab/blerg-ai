// Thin fetch wrapper. Authenticated via a blerg-core-issued bearer token.
// On a 401, redirects the whole page to core's /auth/refresh via
// authClient.ts (a shared module copied from core/web/ — see Task 18) so
// the session can be silently renewed instead of just bouncing the user.
// board no longer has its own /login route to fall back to (see Task 6).

import { getAccessToken, redirectToRefresh } from "./authClient";
import { refreshAlreadyAttempted, markRefreshAttempted } from "./refreshGuard";

export class ApiError extends Error {
  status: number;
  body: Record<string, unknown>;
  constructor(status: number, body: Record<string, unknown>) {
    super((body.error as string) ?? (body.reason as string) ?? `HTTP ${status}`);
    this.status = status;
    this.body = body;
  }
}

export async function api<T = unknown>(
  path: string,
  init?: RequestInit & { json?: unknown },
): Promise<T> {
  const opts: RequestInit = { credentials: "same-origin", ...init };
  if (init?.json !== undefined) {
    opts.method = init.method ?? "POST";
    opts.headers = { "Content-Type": "application/json", ...init.headers };
    opts.body = JSON.stringify(init.json);
  }
  // Only attach the header when a token actually exists — an unconditional
  // template literal sends the literal string "Bearer null", which the server
  // then has to reject as a malformed token rather than as a missing one.
  const token = getAccessToken();
  if (token) opts.headers = { ...opts.headers, Authorization: `Bearer ${token}` };
  const resp = await fetch(path, opts);
  if (resp.status === 401) {
    // Only navigate to core's /auth/refresh once per page load — a second
    // consecutive 401 (e.g. the refresh itself didn't fix things, or two
    // requests raced) must not re-trigger the redirect, or a broken refresh
    // loops the tab forever between board and core.
    if (!refreshAlreadyAttempted()) {
      markRefreshAttempted();
      redirectToRefresh(location.href);
    }
    throw new ApiError(401, { error: "unauthorized" });
  }
  const text = await resp.text();
  const body = text ? JSON.parse(text) : {};
  if (!resp.ok) {
    if (resp.status === 403 && typeof body.error === "string" && body.error.includes("board.admin")) {
      body.error = "Only a board admin can do this.";
    }
    throw new ApiError(resp.status, body);
  }
  return body as T;
}

// ── shapes ──────────────────────────────────────────────────────────────────

export interface Board {
  id: string;
  name: string;
  description: string | null;
  deploy_url: string | null;
  git_base: string;
  // Session models. "" on a role field means "inherit board.model"; "" on
  // model itself means the runner picks. See BoardSettings.
  model: string;
  reviewer_model: string;
  discuss_model: string;
  chat_model: string;
  field_schema: FieldDef[];
  require_repo: boolean;
  gate_enabled: boolean;
  gate_on_unavailable: string;
  gate_on_dispute: string;
  driven_by: string | null;
  // How many cards a board run works at once (default 1); 0 parks the board.
  // See BoardSettings and the run chip in BoardView.
  concurrency: number;
  // What auto-merge does when a PR head reports no CI at all: "required"
  // waits for a green check, "if_present" merges on the review alone. Never
  // about a failing check — red blocks under both.
  ci_policy: string;
  // Who the board's sessions run as: the engine, and whether an automation
  // token (a blerg-core run-sessions agent token) is set. The token itself is
  // write-only — never returned. See BoardSettings' AutomationSection.
  automation_engine: string;
  automation_token_set: boolean;
  automation_token_expires_at: string | null;
  // Whose token it is: the blerg-core account every session this board starts
  // runs as (and whose credential any card-writer can spend).
  automation_account_id: string | null;
  repos: string[] | null;
}

export interface FieldDef {
  key: string;
  label?: string;
  type: string;
  values?: string[];
  display?: string;
  order?: number;
}

export interface Column {
  id: string;
  board_id: string;
  rank: string;
  name: string;
  is_terminal: boolean;
}

export interface CardLink { kind: string; url: string; label: string | null }

export interface Card {
  id: string;
  board_id: string;
  number: number;
  rank: string;
  column_id: string | null;
  type: string;
  title: string;
  body: string | null;
  priority: string;
  size: string | null;
  model: string; // "" = this card's sessions use the board's model
  fields: Record<string, unknown>;
  dedup_key: string | null;
  external_id: string | null;
  archived_at: string | null;
  version: number;
  gate_flag: string | null;
  auto_merge: boolean;
  merged_sha: string | null;
  merged_at: string | null;
  run_attempts: number;
  stuck_at: string | null;
  stale_at: string | null;
  created_at: string;
  updated_at: string;
  repos: string[] | null;
  tags: string[] | null;
  links: CardLink[] | null;
  depends_on: string[] | null;
  // depends_on resolved: the same hard-dependency edges with the blocker's
  // number/title and whether it has landed. Any unsatisfied entry means the
  // dispatcher is skipping this card — which is why it renders.
  blockers: Blocker[] | null;
}

export interface Blocker {
  id: string;
  number: number;
  title: string;
  satisfied: boolean;
}

// pendingBlockers: the dependencies still holding a card out of dispatch.
// Blocked is NOT stuck — a stuck card wants a human, a blocked one is just
// waiting its turn — so the two render differently everywhere.
export function pendingBlockers(card: Pick<Card, "blockers">): Blocker[] {
  return (card.blockers ?? []).filter((b) => !b.satisfied);
}

export interface CardEvent {
  id: number;
  type: string;
  actor: string;
  actor_token_id: string | null;
  from_column_id: string | null;
  to_column_id: string | null;
  data: Record<string, unknown>;
  created_at: string;
}

export interface Review {
  id: string;
  board_id: string;
  submitted_at: string;
  operation: string;
  verdict: string | null;
  reason: string | null;
  policy_applied: string;
  backend: string | null;
  model: string | null;
  latency_ms: number | null;
  payload: Record<string, unknown>;
  card_id: string | null;
  held_expires_at: string | null;
  target_card_id: string | null;
  target_version: number | null;
}

// Body of POST /api/reviews/{id}/resolve on approve. `note` is set when the
// target card's version had drifted from what was held (edited or moved
// since) — the replay still applied, but against content that moved on.
export interface ResolveResult {
  ok: boolean;
  card_id: string;
  note?: string;
}

export interface Token {
  id: string;
  board_id: string | null;
  kind: string;
  label: string;
  capabilities: string[];
  expires_at: string | null;
  revoked_at: string | null;
  last_used_at: string | null;
  created_at: string;
}

// boardSocket subscribes to change pings for one board; onPing → refetch.
// The socket is authenticated via the "bearer" WebSocket subprotocol (a
// browser cannot set Authorization on an upgrade request) — see
// board/internal/api/wsauth.go's wsBearerToken.
export function boardSocket(boardId: string, onPing: () => void): () => void {
  const token = getAccessToken();
  if (!token) return () => {};

  const proto = location.protocol === "https:" ? "wss" : "ws";
  let ws: WebSocket | null = null;
  let closed = false;
  const connect = () => {
    let opened = false;
    ws = new WebSocket(`${proto}://${location.host}/ws?board=${boardId}`, ["bearer", token]);
    ws.onopen = () => {
      opened = true;
    };
    ws.onmessage = onPing;
    ws.onclose = () => {
      if (closed) return;
      // A socket that closed without ever opening was almost certainly
      // refused at the handshake — most likely an expired access token, since
      // the token is captured once here and the socket outlives it.
      // Reconnecting with the same dead token would just loop forever, so go
      // get a new one instead — but only once per page load (the guard is
      // shared with api.ts's 401 handling), so an unreachable server can
      // never turn into a redirect loop.
      if (!opened && !refreshAlreadyAttempted()) {
        markRefreshAttempted();
        redirectToRefresh(location.href);
        return;
      }
      setTimeout(connect, 3000);
    };
  };
  connect();
  return () => {
    closed = true;
    ws?.close();
  };
}
