import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";
import type { Site } from "./Shell";

// MCP connections: GET/POST /api/mcp/connections, PATCH/DELETE /api/mcp/connections/{id}
// (core/internal/api/mcp_handlers.go). A connection is a remote MCP server the person has made
// available to their sessions. Core never returns the secret, and neither does this section: the
// secret input is write-only and cleared as soon as it is sent, so the only thing shown about a
// stored secret is that one exists. Which tools a session may use is chosen in the runner app,
// not here (docs/design/ai-crons.md 4.7). A 400 or 409 carries a short plain-text reason that is
// shown as text (capped, never markup); any other failure gets a generic message.

type Connection = {
  id: string;
  name: string;
  url: string;
  auth_kind: string;
  header_name?: string;
  has_secret: boolean;
  status: string;
  oauth_issuer?: string;
  last_verified_at: string | null;
};
type ConnectionList = { connections: Connection[]; max: number };
type AuthKind = "static" | "oauth" | "none";
type Editing = { id: string; mode: "rename" | "secret" | "remove" };

const SESSION_EXPIRED = "Your session has expired. Reload the page to sign in again.";
const DEFAULT_HEADER = "Authorization";
const DEFAULT_MAX = 20;
const NAME_HINT = "Lowercase letters, digits and hyphens, up to 32 characters, starting with a letter or digit.";

const BAD_SIGN_IN_URL =
  "The server returned what is not a valid https sign-in address, so you were not redirected.";
const NEEDS_AUTH_TEXT =
  "Sign-in expired or was revoked \u2014 sessions and crons that use this connection cannot run until you reconnect";
const ERROR_TEXT = "This connection could not be reached or verified. Sessions and crons that use it may fail.";

// Friendly text for the callback's ?mcp_oauth=error&reason=<word>. Only these words are mapped;
// anything else (including raw query text) gets the generic message and is never displayed.
const OAUTH_REASONS: Record<string, string> = {
  state: "The sign-in link expired or was already used. Please start again.",
  denied: "Sign-in was denied at the server, so nothing was connected.",
  exchange: "Blerg could not finish the sign-in with the server. Please try again.",
  session: "You were not signed in to Blerg when the server sent you back. Sign in and try again.",
  redirect_uri: "The server did not accept Blerg's redirect address.",
  issuer: "The sign-in came from an unexpected sign-in server, so it was rejected.",
  name_taken: "That connection name is already in use. Pick another name and try again.",
  limit: "You have reached the connection limit. Remove one and try again.",
  gone: "The connection no longer exists.",
  internal: "Something went wrong on our side. Please try again.",
};
const OAUTH_GENERIC = "Could not connect. Please try again.";

type Banner = { kind: "success" | "error"; text: string };
type Returned = { banner: Banner | null; connection: string | null };

// readReturn reads the callback's query params once.
function readReturn(): Returned {
  const q = new URLSearchParams(window.location.search);
  const outcome = q.get("mcp_oauth");
  if (outcome === "success") {
    return {
      banner: { kind: "success", text: "Connected. The connection is ready to use." },
      connection: q.get("connection"),
    };
  }
  if (outcome === "error") {
    const reason = q.get("reason") ?? "";
    const known = Object.prototype.hasOwnProperty.call(OAUTH_REASONS, reason);
    return { banner: { kind: "error", text: known ? OAUTH_REASONS[reason] : OAUTH_GENERIC }, connection: null };
  }
  return { banner: null, connection: null };
}

// cleanReturnParams removes the callback params so a refresh does not repeat the banner.
function cleanReturnParams() {
  const q = new URLSearchParams(window.location.search);
  if (!q.has("mcp_oauth")) return;
  q.delete("mcp_oauth");
  q.delete("connection");
  q.delete("reason");
  const qs = q.toString();
  window.history.replaceState(
    window.history.state,
    "",
    window.location.pathname + (qs ? `?${qs}` : "") + window.location.hash,
  );
}

// httpsURL returns the address only when it parses as an https URL.
function httpsURL(raw: unknown): string | null {
  if (typeof raw !== "string") return null;
  try {
    return new URL(raw).protocol === "https:" ? raw : null;
  } catch {
    return null;
  }
}

function issuerHost(issuer?: string): string {
  if (!issuer) return "";
  try {
    return new URL(issuer).host;
  } catch {
    return "";
  }
}

const STATUS: Record<string, { chip: string; label: string }> = {
  ok: { chip: "chip up", label: "OK" },
  needs_auth: { chip: "chip idle", label: "Needs auth" },
  error: { chip: "chip down", label: "Error" },
};

// rejection is core's own words for a 400 or 409 (plain text). Anything else is not trusted to
// carry a user-facing reason.
async function rejection(res: Response): Promise<string | null> {
  if (res.status !== 400 && res.status !== 409) return null;
  try {
    const text = (await res.text()).trim();
    return text === "" ? null : text.slice(0, 200);
  } catch {
    return null;
  }
}

function normalize(raw: unknown): ConnectionList {
  const r = (raw ?? {}) as Partial<ConnectionList>;
  return {
    connections: Array.isArray(r.connections) ? r.connections : [],
    max: typeof r.max === "number" ? r.max : DEFAULT_MAX,
  };
}

function verified(c: Connection): string {
  if (!c.last_verified_at) return "Never verified";
  const d = new Date(c.last_verified_at);
  return Number.isNaN(d.getTime()) ? "Never verified" : `Verified ${d.toLocaleString()}`;
}

function authLabel(c: Connection): string {
  if (c.auth_kind === "oauth") {
    const host = issuerHost(c.oauth_issuer);
    return host ? `OAuth \u00b7 ${host}` : "OAuth";
  }
  if (c.auth_kind === "static") return `Header ${c.header_name || DEFAULT_HEADER}`;
  if (c.auth_kind === "none") return "No authentication";
  return c.auth_kind;
}

export default function McpConnectionsSection({ site }: { site?: Site | null }) {
  const [list, setList] = useState<ConnectionList | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<Editing | null>(null);
  const [editValue, setEditValue] = useState("");

  const [authKind, setAuthKind] = useState<AuthKind>("static");
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [headerName, setHeaderName] = useState(DEFAULT_HEADER);
  const [secret, setSecret] = useState("");
  const [clientId, setClientId] = useState("");
  const [scopes, setScopes] = useState("");
  const [formError, setFormError] = useState<string | null>(null);

  const [returned] = useState<Returned>(readReturn);
  const [banner, setBanner] = useState<Banner | null>(returned.banner);
  const highlightId = returned.connection;

  useEffect(() => {
    cleanReturnParams();
  }, []);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await apiFetch("/api/mcp/connections");
        if (cancelled) return;
        if (res.status === 401) setError(SESSION_EXPIRED);
        else if (!res.ok) setError(`Failed to load MCP connections (${res.status}).`);
        else setList(normalize(await res.json()));
      } catch {
        if (!cancelled) setError("Could not reach the server. Please try again.");
      }
      if (!cancelled) setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const connections = list?.connections ?? [];
  const max = list?.max ?? DEFAULT_MAX;
  const full = connections.length >= max;

  const replace = (c: Connection) =>
    setList((l) => (l ? { ...l, connections: l.connections.map((x) => (x.id === c.id ? c : x)) } : l));

  // send runs one request; on failure it returns the message to show, on success the response.
  const send = async (
    path: string,
    method: string,
    body: unknown,
    verb: string,
  ): Promise<{ res: Response | null; message: string | null }> => {
    try {
      const res = await apiFetch(path, {
        method,
        headers: body === undefined ? undefined : { "Content-Type": "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      if (res.ok) return { res, message: null };
      if (res.status === 401) return { res: null, message: SESSION_EXPIRED };
      return { res: null, message: (await rejection(res)) ?? `Failed to ${verb} the connection (${res.status}).` };
    } catch {
      return { res: null, message: "Could not reach the server. Please try again." };
    }
  };

  // navigate hands the browser to the provider's sign-in page, but only for an https address.
  // It returns the message to show when it refuses.
  const navigate = async (res: Response): Promise<string | null> => {
    let target: string | null;
    try {
      target = httpsURL(((await res.json()) as { authorization_url?: unknown }).authorization_url);
    } catch {
      target = null;
    }
    if (!target) return BAD_SIGN_IN_URL;
    window.location.assign(target);
    return null;
  };

  const startOAuth = async () => {
    setBusy(true);
    setFormError(null);
    const payload: Record<string, string> = { name: name.trim(), url: url.trim() };
    if (clientId.trim()) payload.client_id = clientId.trim();
    if (scopes.trim()) payload.scopes = scopes.trim();
    const { res, message } = await send("/api/mcp/connections/oauth/start", "POST", payload, "start sign-in for");
    setFormError(res ? await navigate(res) : message);
    setBusy(false);
  };

  const reconnect = async (c: Connection) => {
    setBusy(true);
    setError(null);
    const { res, message } = await send(`/api/mcp/connections/${c.id}/reconnect`, "POST", {}, "reconnect");
    setError(res ? await navigate(res) : message);
    setBusy(false);
  };

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    if (busy || !list) return;
    if (authKind === "oauth") {
      await startOAuth();
      return;
    }
    setBusy(true);
    setFormError(null);
    const payload: Record<string, string> = { name: name.trim(), url: url.trim(), auth_kind: authKind };
    if (authKind === "static") {
      payload.header_name = headerName.trim() || DEFAULT_HEADER;
      payload.secret = secret;
    }
    const { res, message } = await send("/api/mcp/connections", "POST", payload, "add");
    if (res) {
      const created = (await res.json()) as Connection;
      setList({ ...list, connections: [...list.connections, created] });
      setName("");
      setUrl("");
      setHeaderName(DEFAULT_HEADER);
    } else {
      setFormError(message);
    }
    // The secret is never kept in the page after it was sent, accepted or not.
    setSecret("");
    setBusy(false);
  };

  const startEdit = (c: Connection, mode: Editing["mode"]) => {
    setError(null);
    setEditing({ id: c.id, mode });
    setEditValue(mode === "rename" ? c.name : "");
  };
  const stopEdit = () => {
    setEditing(null);
    setEditValue("");
  };

  const patch = async (c: Connection, body: Record<string, string>) => {
    setBusy(true);
    setError(null);
    const { res, message } = await send(`/api/mcp/connections/${c.id}`, "PATCH", body, "update");
    if (res) {
      replace((await res.json()) as Connection);
      stopEdit();
    } else {
      setError(message);
      if (body.secret !== undefined) setEditValue("");
    }
    setBusy(false);
  };

  const remove = async (c: Connection) => {
    setBusy(true);
    setError(null);
    const { res, message } = await send(`/api/mcp/connections/${c.id}`, "DELETE", undefined, "remove");
    if (res) {
      setList((l) => (l ? { ...l, connections: l.connections.filter((x) => x.id !== c.id) } : l));
      stopEdit();
    } else {
      setError(message);
    }
    setBusy(false);
  };

  const runnerUrl = site?.runner_url;

  useEffect(() => {
    if (!highlightId || !list) return;
    document.getElementById(`mcp-conn-${highlightId}`)?.scrollIntoView?.({ block: "center" });
  }, [highlightId, list]);

  return (
    <section className="section">
      <p className="eyebrow">Sessions</p>
      <h2 className="section-title">MCP connections</h2>
      <p className="section-help">
        An MCP connection gives your agent sessions access to the tools of a remote MCP server.
        Connections are off for every session until you select them when you start a session or a
        cron, and a session only ever gets the tools you choose there. Choosing the tools is done in
        the{" "}
        {runnerUrl ? (
          <a href={runnerUrl} rel="noreferrer">
            runner app
          </a>
        ) : (
          "runner app"
        )}
        , not here. The secret you enter is stored encrypted and is never shown again.
      </p>

      {loading && <p className="muted">Loading…</p>}
      {error && (
        <p className="field-error" role="alert">
          {error}
        </p>
      )}

      {banner && (
        <div
          data-testid="mcp-oauth-banner"
          role={banner.kind === "error" ? "alert" : "status"}
          className={banner.kind === "error" ? "field-error" : "field-help"}
        >
          <span>{banner.text}</span>{" "}
          <button type="button" className="btn btn-quiet" onClick={() => setBanner(null)}>
            Dismiss
          </button>
        </div>
      )}

      {list && (
        <>
          <p className="field-help">
            {connections.length} of {max} connections used.
          </p>
          {connections.length === 0 ? (
            <p className="muted">
              No MCP connections yet. Add one below, then select it when you start a session.
            </p>
          ) : (
            <ul className="card-list">
              {connections.map((c) => {
                const st = STATUS[c.status] ?? { chip: "chip idle", label: c.status };
                const mode = editing?.id === c.id ? editing.mode : null;
                return (
                  <li
                    className="card"
                    key={c.id}
                    id={`mcp-conn-${c.id}`}
                    data-highlight={highlightId === c.id ? "true" : undefined}
                    style={highlightId === c.id ? { outline: "2px solid currentColor" } : undefined}
                  >
                    <div className="card-head">
                      <h3 className="card-title mono">{c.name}</h3>
                      <span className={st.chip}>
                        <span className="dot" aria-hidden="true" />
                        <span className="label">{st.label}</span>
                      </span>
                    </div>
                    <p className="card-help mono">{c.url}</p>
                    <p className="field-help" data-testid={`mcp-auth-${c.id}`}>
                      {authLabel(c)}
                      {c.auth_kind === "static" && c.has_secret ? " (secret stored)" : ""}. {verified(c)}.
                    </p>

                    {c.auth_kind === "oauth" && c.status === "needs_auth" && (
                      <p className="field-error">{NEEDS_AUTH_TEXT}</p>
                    )}
                    {c.auth_kind === "oauth" && c.status === "error" && <p className="field-error">{ERROR_TEXT}</p>}

                    {mode === null && (
                      <div className="card-actions">
                        {c.auth_kind === "oauth" && (
                          <button
                            type="button"
                            className={c.status === "needs_auth" ? "btn btn-primary" : "btn"}
                            disabled={busy}
                            aria-label={`Reconnect ${c.name}`}
                            onClick={() => void reconnect(c)}
                          >
                            Reconnect
                          </button>
                        )}
                        <button
                          type="button"
                          className="btn"
                          disabled={busy}
                          aria-label={`Rename ${c.name}`}
                          onClick={() => startEdit(c, "rename")}
                        >
                          Rename
                        </button>
                        {c.auth_kind === "static" && (
                          <button
                            type="button"
                            className="btn"
                            disabled={busy}
                            aria-label={`Replace secret for ${c.name}`}
                            onClick={() => startEdit(c, "secret")}
                          >
                            Replace secret
                          </button>
                        )}
                        <button
                          type="button"
                          className="btn btn-quiet"
                          disabled={busy}
                          aria-label={`Remove ${c.name}`}
                          onClick={() => startEdit(c, "remove")}
                        >
                          Remove
                        </button>
                      </div>
                    )}

                    {mode === "rename" && (
                      <form
                        className="card-form"
                        onSubmit={(e) => {
                          e.preventDefault();
                          void patch(c, { name: editValue.trim() });
                        }}
                      >
                        <label className="field" htmlFor={`mcp-rename-${c.id}`}>
                          <span className="field-label">New name</span>
                          <input
                            id={`mcp-rename-${c.id}`}
                            className="mono"
                            value={editValue}
                            onChange={(e) => setEditValue(e.target.value)}
                            maxLength={32}
                            autoComplete="off"
                          />
                        </label>
                        <p className="field-help">{NAME_HINT}</p>
                        <div className="card-actions">
                          <button
                            type="submit"
                            className="btn btn-primary"
                            disabled={busy || editValue.trim() === "" || editValue.trim() === c.name}
                          >
                            {busy ? "Saving…" : "Save name"}
                          </button>
                          <button type="button" className="btn btn-quiet" disabled={busy} onClick={stopEdit}>
                            Cancel
                          </button>
                        </div>
                      </form>
                    )}

                    {mode === "secret" && (
                      <form
                        className="card-form"
                        onSubmit={(e) => {
                          e.preventDefault();
                          void patch(c, { secret: editValue });
                        }}
                      >
                        <label className="field" htmlFor={`mcp-secret-${c.id}`}>
                          <span className="field-label">New secret</span>
                          <input
                            id={`mcp-secret-${c.id}`}
                            type="password"
                            className="mono"
                            value={editValue}
                            onChange={(e) => setEditValue(e.target.value)}
                            autoComplete="off"
                          />
                        </label>
                        <div className="card-actions">
                          <button type="submit" className="btn btn-primary" disabled={busy || editValue === ""}>
                            {busy ? "Saving…" : "Save secret"}
                          </button>
                          <button type="button" className="btn btn-quiet" disabled={busy} onClick={stopEdit}>
                            Cancel
                          </button>
                        </div>
                      </form>
                    )}

                    {mode === "remove" && (
                      <div role="group" aria-label={`Confirm removing ${c.name}`}>
                        <p className="field-help">
                          Remove {c.name}? Sessions and crons that use it lose access to its tools.
                          {c.auth_kind === "oauth" &&
                            " The access you granted is also revoked at the provider where the provider supports it."}
                        </p>
                        <div className="card-actions">
                          <button
                            type="button"
                            className="btn btn-primary"
                            disabled={busy}
                            onClick={() => void remove(c)}
                          >
                            {busy ? "Removing…" : "Confirm remove"}
                          </button>
                          <button type="button" className="btn btn-quiet" disabled={busy} onClick={stopEdit}>
                            Cancel
                          </button>
                        </div>
                      </div>
                    )}
                  </li>
                );
              })}
            </ul>
          )}

          {full ? (
            <p className="field-help">
              You have reached the limit of {max} connections. Remove one to add another.
            </p>
          ) : (
            <form className="card-form" onSubmit={add}>
              <label className="field" htmlFor="mcp-kind">
                <span className="field-label">Authentication</span>
                <select id="mcp-kind" value={authKind} onChange={(e) => setAuthKind(e.target.value as AuthKind)}>
                  <option value="static">Token (static)</option>
                  <option value="oauth">Sign in (OAuth)</option>
                  <option value="none">No credential</option>
                </select>
              </label>
              <label className="field" htmlFor="mcp-name">
                <span className="field-label">Name</span>
                <input
                  id="mcp-name"
                  className="mono"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="my-server"
                  maxLength={32}
                  autoComplete="off"
                  aria-describedby="mcp-name-hint"
                />
              </label>
              <p className="field-help" id="mcp-name-hint">
                {NAME_HINT}
              </p>
              <label className="field" htmlFor="mcp-url">
                <span className="field-label">Server URL (https)</span>
                <input
                  id="mcp-url"
                  className="mono"
                  type="url"
                  value={url}
                  onChange={(e) => setUrl(e.target.value)}
                  placeholder="https://mcp.example.com/mcp"
                  autoComplete="off"
                />
              </label>
              {authKind === "static" && (
                <>
                  <label className="field" htmlFor="mcp-header">
                    <span className="field-label">Header name</span>
                    <input
                      id="mcp-header"
                      className="mono"
                      value={headerName}
                      onChange={(e) => setHeaderName(e.target.value)}
                      autoComplete="off"
                    />
                  </label>
                  <label className="field" htmlFor="mcp-secret">
                    <span className="field-label">Secret (header value)</span>
                    <input
                      id="mcp-secret"
                      type="password"
                      className="mono"
                      value={secret}
                      onChange={(e) => setSecret(e.target.value)}
                      autoComplete="off"
                    />
                  </label>
                </>
              )}
              {authKind === "oauth" && (
                <>
                  <label className="field" htmlFor="mcp-client-id">
                    <span className="field-label">
                      Client ID (only if the server does not register clients automatically)
                    </span>
                    <input
                      id="mcp-client-id"
                      className="mono"
                      value={clientId}
                      onChange={(e) => setClientId(e.target.value)}
                      autoComplete="off"
                    />
                  </label>
                  <label className="field" htmlFor="mcp-scopes">
                    <span className="field-label">Scopes (optional, separated by spaces)</span>
                    <input
                      id="mcp-scopes"
                      className="mono"
                      value={scopes}
                      onChange={(e) => setScopes(e.target.value)}
                      autoComplete="off"
                    />
                  </label>
                  <p className="field-help">
                    You will be sent to the server's sign-in page and brought back here when you are done.
                  </p>
                </>
              )}
              {formError && (
                <p className="field-error" role="alert">
                  {formError}
                </p>
              )}
              <div className="card-actions">
                <button
                  type="submit"
                  className="btn btn-primary"
                  disabled={busy || name.trim() === "" || url.trim() === "" || (authKind === "static" && secret === "")}
                >
                  {authKind === "oauth" ? (busy ? "Connecting…" : "Connect") : busy ? "Adding…" : "Add connection"}
                </button>
              </div>
            </form>
          )}
        </>
      )}
    </section>
  );
}
