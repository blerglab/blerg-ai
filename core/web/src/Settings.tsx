import { useEffect, useState } from "react";
import { Navigate } from "react-router-dom";
import { apiFetch } from "./apiFetch";
import { getAccessToken } from "./authClient";
import { mustChangePassword } from "./claims";
import PluginsSection from "./PluginsSection";
import McpConnectionsSection from "./McpConnectionsSection";
import Shell, { useSite, type Site } from "./Shell";

// Settings: per-credential paste-in. Talks to POST/GET/DELETE /api/credentials (Task 14),
// which is gated on a valid human access token via `Authorization: Bearer` and never returns
// ciphertext/plaintext from GET — only {engine, updated_at} pairs. This page mirrors that: it
// tracks "configured"/"not configured" status per kind and NEVER re-displays a credential
// once submitted, even transiently — the paste-in field is cleared immediately on a
// successful save, and there is no code path anywhere here that reads a stored credential's
// value back from the server.
//
// Two groups: the engines actually wired into cluster runtime (see runner/frontend's
// LaunchSheet, whose engine picker recognizes "claude", "codex", "hermes" — OpenClaw is
// listed there too, but explicitly noted as not wired into either sandbox, so it has no slot
// here), and git access, where a personal GitHub or GitLab token lets cluster sessions clone
// and push as the launching user instead of as a shared operator identity, and lets the runner's
// launch sheet list that user's own repositories.
//
// The API field is still named "engine" for compatibility; the UI calls them credentials.
//
// The git kinds are the git providers the runner registers (runner/internal/gitprovider) and
// core accepts (core/internal/api/credential_handlers.go gitCredentialKinds): a new provider is
// one more entry in KINDS, KIND_LABELS, GIT_KINDS and KIND_HELP.
const KINDS = ["claude", "codex", "hermes", "github", "gitlab"] as const;
type Kind = (typeof KINDS)[number];

const KIND_LABELS: Record<Kind, string> = {
  claude: "Claude",
  codex: "Codex",
  hermes: "Hermes",
  github: "GitHub",
  gitlab: "GitLab",
};

const GIT_KINDS: readonly Kind[] = ["github", "gitlab"];

const GROUPS: { eyebrow: string; kinds: readonly Kind[] }[] = [
  { eyebrow: "Engines", kinds: ["claude", "codex", "hermes"] },
  { eyebrow: "Git", kinds: GIT_KINDS },
];

const ANTHROPIC_KEYS_URL = "https://console.anthropic.com/settings/keys";
const GITHUB_TOKEN_URL = "https://github.com/settings/personal-access-tokens/new";
const GITLAB_TOKEN_URL = "https://gitlab.com/-/user_settings/personal_access_tokens";

// "How to get this" copy, one paragraph per kind, straight from the design spec §2.
const KIND_HELP: Record<Kind, React.ReactNode> = {
  claude: (
    <>
      Run <code className="mono">claude setup-token</code> on a machine where you're logged in
      and paste the token it prints (starts with <code className="mono">sk-ant-oat</code>). Or
      paste an API key from the{" "}
      <a href={ANTHROPIC_KEYS_URL} target="_blank" rel="noopener noreferrer">
        Anthropic console
      </a>{" "}
      (starts with <code className="mono">sk-ant-api</code>) to bill an organisation instead.
    </>
  ),
  codex: (
    <>
      Paste the contents of <code className="mono">~/.codex/auth.json</code> from a machine
      where <code className="mono">codex login</code> has succeeded.
    </>
  ),
  hermes: (
    <>
      Paste the contents of <code className="mono">~/.hermes/.env</code> from a machine where
      Hermes is set up.
    </>
  ),
  github: (
    <>
      Create a fine-grained personal access token at{" "}
      <a href={GITHUB_TOKEN_URL} target="_blank" rel="noopener noreferrer">
        {GITHUB_TOKEN_URL}
      </a>
      . Resource owner: your account or an organisation you own; repository access: the repos
      sessions may work on; permissions: Contents read and write, Metadata read. The launch sheet
      lists the repos it can see, and it clones private ones for you. Cluster sessions also push
      with it; a desktop session never does — a Local sandbox session can commit but not push
      (push from your machine), and a This machine session pushes with your machine's own git
      login.
    </>
  ),
  gitlab: (
    <>
      Create a personal access token at{" "}
      <a href={GITLAB_TOKEN_URL} target="_blank" rel="noopener noreferrer">
        {GITLAB_TOKEN_URL}
      </a>{" "}
      (avatar → Edit profile → Access tokens). Scopes: <code className="mono">read_api</code>{" "}
      so the launch sheet can list your projects, and{" "}
      <code className="mono">read_repository</code> plus{" "}
      <code className="mono">write_repository</code> so private projects can be cloned for you
      (and cluster sessions can push; a desktop session pushes, if at all, with your machine's own
      git login — a Local sandbox session can commit but not push).
      Projects inside subgroups are not listed yet.
    </>
  ),
};

type CredentialSummary = { engine: string; updated_at: string };

// SESSION_EXPIRED is what a 401 that reached a caller means. apiFetch normally answers a 401 by
// navigating to core's /auth/refresh and never resolving, so a 401 only lands here when the
// refresh guard has already spent its redirect for this page load (refreshGuard.ts) — which is
// also what happens to the SECOND of this page's two concurrent loads (credentials and tokens)
// when both come back 401: the first triggers the redirect, the second is handed its 401 back.
// Rendering "Failed to load … (401)" for that is both noise and a lie about the cause; the
// honest instruction is to reload.
const SESSION_EXPIRED = "Your session has expired. Reload the page to sign in again.";

// ── agent tokens (spec §2) ───────────────────────────────────────────────────
//
// Agent tokens are the credentials a user mints here so an external tool — a CLI, a CI job,
// another agent — can act as them against one component without a password or a shared static
// key. The same one-way rule as credentials above applies, and harder: core returns the token
// value exactly once, in POST /api/tokens' 201 body (token_handlers.go stores only a hash), so
// this component holds it in state only until "Done" and there is no code path that can ask for
// it again. GET /api/tokens has no field for it at all.
//
// The preset list is core's closed list (identity.AgentTokenPresets). It is duplicated here
// rather than fetched because the copy shown to a human ("what will this token let a tool do?")
// is UI text, not API data — and a select that cannot render until a fetch resolves is worse
// than one that may, in the worst case, lag a new preset by a release. An unknown audience
// coming back from the API still renders (see presetForAud).
const PRESETS = [
  {
    name: "run-sessions",
    aud: "blerg-runner",
    summary: "Start and drive agent sessions on the runner (REST and MCP)",
  },
  {
    name: "board",
    aud: "blerg-board",
    summary: "Read and write cards on the board (REST and MCP)",
  },
  {
    name: "platform",
    aud: "blerg-core",
    summary: "Read your identity and credential status on core",
  },
] as const;

type PresetName = (typeof PRESETS)[number]["name"];

const DEFAULT_EXPIRY_DAYS = 90;

// GET /api/tokens' entry (api.tokenSummaryResponse): metadata only, never a value.
type TokenSummary = {
  id: string;
  name: string;
  aud: string;
  caps: string[];
  created_at: string;
  expires_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
};

// POST /api/tokens' 201 body (api.createdTokenResponse) — the only shape with a `token` field.
type CreatedToken = TokenSummary & { token: string };

// presetForAud names the preset a listed token came from. The list endpoint returns the
// audience, not the preset, because the audience is what the token actually carries; falling
// back to the raw audience keeps a token minted under a preset this build doesn't know about
// (older/newer core) visible and revocable instead of blank.
function presetForAud(aud: string): string {
  return PRESETS.find((p) => p.aud === aud)?.name ?? aud;
}

// baseUrlFor is where a token of this preset is meant to be sent: the component's
// browser-facing URL from GET /api/site. Core is its own origin, and any missing site field
// falls back to the origin too — a slightly wrong example beats no example, and the user can
// see and fix a host they recognise.
// Keyed on the AUDIENCE of the token that was actually minted, not on the select's current
// value — the user may change the select while the reveal panel is still open, and the example
// must keep pointing at the component the revealed token can talk to.
function baseUrlForAud(aud: string, site: Site | null): string {
  const origin = typeof location !== "undefined" ? location.origin : "";
  if (aud === "blerg-runner") return site?.runner_url || origin;
  if (aud === "blerg-board") return site?.board_url || origin;
  return origin;
}

// probePathForAud is the route the "try it" line calls: an AUTHENTICATED one, so a 200 proves
// the token works. /agents would answer 200 for a wrong, expired or revoked token just as
// happily — it is public — which makes it exactly the wrong thing to test a credential with.
// Each component's cheapest read the token's preset already grants:
//   blerg-runner → /api/runner/me (credential introspection)
//   blerg-core   → /api/me        (the same, on core)
//   blerg-board  → /api/boards    (the boards this credential can see; card.read)
function probePathForAud(aud: string): string {
  if (aud === "blerg-runner") return "/api/runner/me";
  if (aud === "blerg-board") return "/api/boards";
  return "/api/me";
}

// backendError is the server's own explanation of a failure, when it gave one. Core answers
// most rejections as plain text and a few — the live-token cap above all — as JSON `{error}`,
// which is the message a user can act on ("revoke one first"). Only that shape is trusted:
// anything else (a proxy's HTML page, a bare array) falls back to the generic message, so the
// UI can never render an arbitrary body as if core had said it.
async function backendError(res: Response): Promise<string | null> {
  try {
    const body: unknown = await res.json();
    const msg = (body as { error?: unknown } | null)?.error;
    if (typeof msg !== "string") return null;
    const trimmed = msg.trim();
    return trimmed === "" ? null : trimmed.slice(0, 200);
  } catch {
    return null;
  }
}

// Dates come back RFC3339. Rendered in the viewer's locale; an unparseable value is shown raw
// rather than as "Invalid Date".
function formatDate(value: string | null): string {
  if (!value) return "Never";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

type TokenStatus = "Active" | "Revoked" | "Expired";

// Revocation wins over expiry: a token revoked before it expired is revoked, and that is the
// state the owner acted on.
function statusOf(t: TokenSummary, now: number): TokenStatus {
  if (t.revoked_at) return "Revoked";
  const exp = new Date(t.expires_at).getTime();
  if (!Number.isNaN(exp) && exp <= now) return "Expired";
  return "Active";
}

const STATUS_CHIP: Record<TokenStatus, string> = {
  Active: "chip up",
  Revoked: "chip down",
  Expired: "chip idle",
};

// loadTokens fetches the caller's agent tokens. Exactly one of the fields is set.
async function loadTokens(): Promise<{ tokens?: TokenSummary[]; loadedAt?: number; error?: string }> {
  try {
    const res = await apiFetch("/api/tokens");
    if (res.status === 401) return { error: SESSION_EXPIRED };
    if (!res.ok) return { error: `Failed to load agent tokens (${res.status}).` };
    const list = await res.json();
    // Defensive: only an array is a token list. A non-array body (an error envelope, a
    // proxy's HTML) must not crash the whole Settings page.
    return { tokens: Array.isArray(list) ? (list as TokenSummary[]) : [], loadedAt: Date.now() };
  } catch {
    return { error: "Could not reach the server. Please try again." };
  }
}

function AgentTokens({ site }: { site: Site | null }) {
  const [tokens, setTokens] = useState<TokenSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [preset, setPreset] = useState<PresetName>("run-sessions");
  const [days, setDays] = useState(String(DEFAULT_EXPIRY_DAYS));
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<string | null>(null);
  // The one-time reveal. Cleared by "Done" and by nothing else re-populating it.
  const [created, setCreated] = useState<CreatedToken | null>(null);
  const [copied, setCopied] = useState(false);

  // When the list was last loaded: what "Expired" is measured against.
  const [now, setNow] = useState(() => Date.now());

  const show = (r: Awaited<ReturnType<typeof loadTokens>>) => {
    if (r.tokens) {
      setTokens(r.tokens);
      setNow(r.loadedAt ?? 0);
      setError(null);
    } else if (r.error) {
      setError(r.error);
    }
    setLoading(false);
  };

  const refresh = async () => {
    setLoading(true);
    show(await loadTokens());
  };

  // First load: the initial state is already "loading".
  useEffect(() => {
    let cancelled = false;
    loadTokens().then((r) => {
      if (cancelled) return;
      if (r.tokens) {
        setTokens(r.tokens);
        setNow(r.loadedAt ?? 0);
        setError(null);
      } else if (r.error) {
        setError(r.error);
      }
      setLoading(false);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) return;
    setCreating(true);
    setError(null);
    try {
      const res = await apiFetch("/api/tokens", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: trimmed,
          preset,
          expires_in_days: Number(days) || DEFAULT_EXPIRY_DAYS,
        }),
      });
      if (!res.ok) {
        // Prefer core's own words: at the live-token cap it says which limit was hit and
        // what to do about it, which "(409)" alone does not.
        const detail = await backendError(res);
        setError(detail ?? `Failed to create token (${res.status}).`);
        return;
      }
      const body: CreatedToken = await res.json();
      setCreated(body);
      setCopied(false);
      setName("");
      await refresh();
    } catch {
      setError("Could not reach the server. Please try again.");
    } finally {
      setCreating(false);
    }
  };

  const copy = () => {
    if (!created) return;
    // Guarded: clipboard access is absent on insecure origins and in some browsers, and
    // writeText rejects when the document isn't focused. Neither is worth an exception.
    try {
      const write = navigator?.clipboard?.writeText;
      if (!write) return;
      const result = write.call(navigator.clipboard, created.token) as Promise<void> | undefined;
      setCopied(true);
      result?.catch?.(() => setCopied(false));
    } catch {
      setCopied(false);
    }
  };

  const revoke = async (id: string) => {
    setRevoking(id);
    setError(null);
    try {
      const res = await apiFetch(`/api/tokens/${id}`, { method: "DELETE" });
      if (!res.ok) {
        setError(`Failed to revoke token (${res.status}).`);
        return;
      }
      await refresh();
    } catch {
      setError("Could not reach the server. Please try again.");
    } finally {
      setRevoking(null);
    }
  };

  const curlLine = created
    ? `curl -H "Authorization: Bearer ${created.token}" ${baseUrlForAud(created.aud, site)}${probePathForAud(created.aud)}`
    : "";

  return (
    <section className="section">
      <p className="eyebrow">Automation</p>
      <h2 className="section-title">Agent tokens</h2>
      <p className="section-help">
        Tokens for tools and agents that act as you. Each is scoped to one component; shown once.
        A run-sessions token can only see sessions it started; admins see all. What a token may do
        is fixed when it is created — changing your role later does not change an existing token,
        so revoke it instead.
      </p>
      {error && (
        <p className="field-error" role="alert">
          {error}
        </p>
      )}

      <form className="card card-form" onSubmit={submit}>
        <label className="field" htmlFor="token-name">
          <span className="field-label">Token name</span>
          <input
            id="token-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="laptop cli"
            maxLength={64}
          />
        </label>
        <label className="field" htmlFor="token-preset">
          <span className="field-label">Preset</span>
          <select
            id="token-preset"
            value={preset}
            onChange={(e) => setPreset(e.target.value as PresetName)}
          >
            {PRESETS.map((p) => (
              <option key={p.name} value={p.name}>
                {p.name} — {p.summary}
              </option>
            ))}
          </select>
        </label>
        <label className="field" htmlFor="token-days">
          <span className="field-label">Expires in (days)</span>
          <input
            id="token-days"
            type="number"
            min={1}
            max={365}
            value={days}
            onChange={(e) => setDays(e.target.value)}
          />
        </label>
        <div className="card-actions">
          <button type="submit" className="btn btn-primary" disabled={creating || name.trim() === ""}>
            {creating ? "Creating…" : "Create token"}
          </button>
        </div>
      </form>

      {created && (
        <div className="card token-reveal">
          <div className="card-head">
            <h3 className="card-title">New token</h3>
            <span className="chip active">
              <span className="dot" aria-hidden="true" />
              <span className="label">Shown once</span>
            </span>
          </div>
          <p className="card-help">
            Copy it now — <strong>{created.name}</strong> is shown this one time and cannot be
            retrieved again. If you lose it, revoke it and mint another.
          </p>
          <label className="field" htmlFor="token-value">
            <span className="field-label">New token</span>
            <input id="token-value" className="mono" readOnly value={created.token} />
          </label>
          <p className="card-help">Try it:</p>
          <pre className="token-curl mono">{curlLine}</pre>
          <div className="card-actions">
            <button type="button" className="btn" onClick={copy}>
              Copy
            </button>
            {copied && <span className="field-help">Copied</span>}
            <button type="button" className="btn btn-quiet" onClick={() => setCreated(null)}>
              Done
            </button>
          </div>
        </div>
      )}

      {loading && <p className="muted">Loading…</p>}
      {/* "No tokens yet" is a claim about the account, so only say it when the list actually
          loaded — a failed load has no idea how many tokens exist. */}
      {!loading && !error && tokens.length === 0 && <p className="muted">No agent tokens yet.</p>}
      {tokens.length > 0 && (
        <div className="table-wrap">
          <table className="token-table">
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">Preset</th>
                <th scope="col">Created</th>
                <th scope="col">Expires</th>
                <th scope="col">Last used</th>
                <th scope="col">Status</th>
                <th scope="col">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {tokens.map((t) => {
                const status = statusOf(t, now);
                return (
                  <tr key={t.id}>
                    <td>{t.name}</td>
                    <td>
                      {presetForAud(t.aud)}
                      <br />
                      <span className="mono token-aud">{t.aud}</span>
                    </td>
                    <td>{formatDate(t.created_at)}</td>
                    <td>{formatDate(t.expires_at)}</td>
                    <td>{formatDate(t.last_used_at)}</td>
                    <td>
                      <span className={STATUS_CHIP[status]}>
                        <span className="dot" aria-hidden="true" />
                        <span className="label">{status}</span>
                      </span>
                    </td>
                    <td>
                      {/* A revoked token is already dead; revoking it again would be a no-op
                          request against an endpoint that is deliberately idempotent. */}
                      {!t.revoked_at && (
                        <button
                          type="button"
                          className="btn btn-quiet"
                          onClick={() => revoke(t.id)}
                          disabled={revoking === t.id}
                        >
                          {revoking === t.id ? "Revoking…" : "Revoke"}
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

const emptyDrafts = (): Record<Kind, string> =>
  Object.fromEntries(KINDS.map((k) => [k, ""])) as Record<Kind, string>;

// loadCredentials fetches which credentials are configured. Exactly one of the fields is set.
async function loadCredentials(): Promise<{ configured?: Set<string>; error?: string }> {
  try {
    // apiFetch attaches the bearer token and, on a 401, sends the page to
    // core's /auth/refresh instead of resolving — so the branches below only
    // ever see a real, non-auth failure.
    const res = await apiFetch("/api/credentials");
    if (res.status === 401) return { error: SESSION_EXPIRED };
    if (!res.ok) return { error: `Failed to load credential status (${res.status}).` };
    const list: CredentialSummary[] = await res.json();
    return { configured: new Set(list.map((c) => c.engine)) };
  } catch {
    return { error: "Could not reach the server. Please try again." };
  }
}

export default function Settings() {
  // R7/I-7: same UI-routing convenience as App.tsx's Home — the server (POST/GET/DELETE
  // /api/credentials gated on "card.read", see core/internal/api/router.go) is what actually
  // stops a password-change-only token from doing anything here. The screen itself is a
  // separate component so its hooks are never skipped by this early return.
  if (mustChangePassword(getAccessToken())) {
    return <Navigate to="/change-password" replace />;
  }
  return <SettingsScreen />;
}

function SettingsScreen() {

  const [configured, setConfigured] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [drafts, setDrafts] = useState<Record<Kind, string>>(emptyDrafts);
  const [submitting, setSubmitting] = useState<Kind | null>(null);
  const [removing, setRemoving] = useState<Kind | null>(null);

  const site = useSite();

  const refresh = async () => {
    setLoading(true);
    setError(null);
    const r = await loadCredentials();
    if (r.configured) setConfigured(r.configured);
    else if (r.error) setError(r.error);
    setLoading(false);
  };

  // First load: the initial state is already "loading, no error".
  useEffect(() => {
    let cancelled = false;
    loadCredentials().then((r) => {
      if (cancelled) return;
      if (r.configured) setConfigured(r.configured);
      else if (r.error) setError(r.error);
      setLoading(false);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  const submit = async (kind: Kind, e: React.FormEvent) => {
    e.preventDefault();
    const credential = drafts[kind].trim();
    if (!credential) return;
    setSubmitting(kind);
    setError(null);
    try {
      const res = await apiFetch("/api/credentials", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        // The wire field is still "engine" — see the header comment.
        body: JSON.stringify({ engine: kind, credential }),
      });
      if (!res.ok) {
        setError(`Failed to save ${KIND_LABELS[kind]} credential (${res.status}).`);
        return;
      }
      // Clear the draft immediately — the pasted value never lingers in state once saved.
      setDrafts((d) => ({ ...d, [kind]: "" }));
      await refresh();
    } catch {
      setError("Could not reach the server. Please try again.");
    } finally {
      setSubmitting(null);
    }
  };

  const remove = async (kind: Kind) => {
    setRemoving(kind);
    setError(null);
    try {
      const res = await apiFetch(`/api/credentials/${kind}`, { method: "DELETE" });
      if (!res.ok) {
        setError(`Failed to remove ${KIND_LABELS[kind]} credential (${res.status}).`);
        return;
      }
      await refresh();
    } catch {
      setError("Could not reach the server. Please try again.");
    } finally {
      setRemoving(null);
    }
  };

  return (
    <Shell site={site} aside={<a className="btn btn-quiet" href="/app">Home</a>}>
      <section className="section">
        <p className="eyebrow">Settings</p>
        <h2 className="section-title">Credentials</h2>
        <p className="section-help">
          Used by cluster-runtime sessions, which run as you. Desktop sessions use them differently by kind: the engine credentials aren't needed there, because a desktop session uses the engine login already on your machine; the GitHub and GitLab tokens are, because the launch sheet lists your repositories with them and clones a private one with them (always in a Local sandbox session; on This machine only if the daemon's owner allowed it).
        </p>
        {loading && <p className="muted">Loading…</p>}
        {error && <p className="field-error" role="alert">{error}</p>}
        {GROUPS.map((group) => (
          <div className="group" key={group.eyebrow}>
            <p className="eyebrow">{group.eyebrow}</p>
            <ul className="card-list">
              {group.kinds.map((kind) => {
                const isConfigured = configured.has(kind);
                return (
                  <li key={kind} className="card">
                    <div className="card-head">
                      <h3 className="card-title">{KIND_LABELS[kind]}</h3>
                      <span className={isConfigured ? "chip up" : "chip idle"}>
                        <span className="dot" aria-hidden="true" />
                        <span className="label">{isConfigured ? "Configured" : "Not configured"}</span>
                      </span>
                    </div>
                    <p className="card-help">{KIND_HELP[kind]}</p>
                    <form onSubmit={(e) => submit(kind, e)} className="card-form">
                      <label className="field" htmlFor={`credential-${kind}`}>
                        <span className="field-label">{KIND_LABELS[kind]} credential</span>
                      </label>
                      <textarea
                        id={`credential-${kind}`}
                        value={drafts[kind]}
                        onChange={(e) =>
                          setDrafts((d) => ({ ...d, [kind]: e.target.value }))
                        }
                        placeholder="Paste credential…"
                      />
                      <div className="card-actions">
                        <button type="submit" className="btn btn-primary" disabled={submitting === kind || drafts[kind].trim() === ""}>
                          {submitting === kind
                            ? "Saving…"
                            : isConfigured
                              ? "Replace credential"
                              : "Save credential"}
                        </button>
                        {isConfigured && (
                          <button
                            type="button"
                            className="btn btn-quiet"
                            onClick={() => remove(kind)}
                            disabled={removing === kind}
                          >
                            {removing === kind ? "Removing…" : "Remove"}
                          </button>
                        )}
                      </div>
                    </form>
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </section>
      <PluginsSection />
      <McpConnectionsSection site={site} />
      <AgentTokens site={site} />
    </Shell>
  );
}
