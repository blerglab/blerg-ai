import { useState } from "react";
import { apiFetch } from "./apiFetch";
import type { ManagedCredentialSummary } from "./ManagedCredentials";

// BLG-460: one read-only token per GitHub/GitLab owner (a person or an organization), stored in
// the same write-only credential vault as every other credential here (POST/GET/DELETE
// /api/credentials) under a kind shaped "git-readonly:<provider>:<owner>" — core's
// gitReadOnlyKindShape validates it server-side. A cluster worker clones from this instead of
// the owner's own write-capable personal credential or the operator's shared one. Like every
// other credential on this page, the pasted value is never re-displayed: only {owner, provider,
// last_used_at} come back from the server.

export const GIT_READONLY_PREFIX = "git-readonly:";

type Provider = "github" | "gitlab";
const PROVIDERS: Provider[] = ["github", "gitlab"];
const PROVIDER_LABEL: Record<Provider, string> = { github: "GitHub", gitlab: "GitLab" };

type GitReadOnlyEntry = {
  engine: string;
  provider: Provider;
  owner: string;
  lastUsedAt: string | null;
};

function isProvider(value: string): value is Provider {
  return (PROVIDERS as string[]).includes(value);
}

// parseGitReadOnlyEntries picks the git-readonly:* rows out of the page's one shared
// /api/credentials list and parses each into {provider, owner}. A row whose shape doesn't
// parse (an owner with no provider, an unknown provider) is skipped rather than shown mangled —
// core's own validation never lets one through, so this is defense in depth, not the primary
// check.
export function parseGitReadOnlyEntries(summaries: ManagedCredentialSummary[]): GitReadOnlyEntry[] {
  const out: GitReadOnlyEntry[] = [];
  for (const s of summaries) {
    if (!s.engine.startsWith(GIT_READONLY_PREFIX)) continue;
    const rest = s.engine.slice(GIT_READONLY_PREFIX.length);
    const sep = rest.indexOf(":");
    if (sep < 0) continue;
    const provider = rest.slice(0, sep);
    const owner = rest.slice(sep + 1);
    if (!isProvider(provider) || owner === "") continue;
    out.push({ engine: s.engine, provider, owner, lastUsedAt: s.last_used_at ?? null });
  }
  out.sort((a, b) => a.owner.localeCompare(b.owner) || a.provider.localeCompare(b.provider));
  return out;
}

// Dates come back RFC3339, same convention as the rest of this page (Settings.tsx's own
// formatDate). An unparseable value is shown raw rather than as "Invalid Date".
function formatLastUsed(value: string | null): string {
  if (!value) return "Never used";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

export default function GitReadOnlyTokensSection({ summaries, onChanged }: {
  summaries: ManagedCredentialSummary[];
  onChanged: () => Promise<boolean>;
}) {
  const entries = parseGitReadOnlyEntries(summaries);
  const [provider, setProvider] = useState<Provider>("github");
  const [owner, setOwner] = useState("");
  const [token, setToken] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmedOwner = owner.trim();
    const trimmedToken = token.trim();
    if (!trimmedOwner || !trimmedToken) return;
    setSubmitting(true);
    setError(null);
    try {
      const engine = `${GIT_READONLY_PREFIX}${provider}:${trimmedOwner}`;
      const res = await apiFetch("/api/credentials", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ engine, credential: trimmedToken }),
      });
      if (!res.ok) {
        // The server's body is the exact reason (BLG-460: a classic-token refusal, a GitLab
        // scope refusal, an invalid owner, ...) — shown verbatim rather than just the status
        // code, so the person knows what to fix.
        const reason = await res.text().catch(() => "");
        setError(reason ? `Failed to save the read-only token for ${trimmedOwner}: ${reason}` : `Failed to save the read-only token for ${trimmedOwner} (${res.status}).`);
        return;
      }
      // Cleared immediately on success — the pasted value never lingers in state.
      setOwner("");
      setToken("");
      if (!(await onChanged())) setError("Saved, but the list could not be refreshed. Reload the page.");
    } catch {
      setError("Could not reach the server. Please try again.");
    } finally {
      setSubmitting(false);
    }
  };

  const remove = async (entry: GitReadOnlyEntry) => {
    setRemoving(entry.engine);
    setError(null);
    try {
      const res = await apiFetch(`/api/credentials/${encodeURIComponent(entry.engine)}`, { method: "DELETE" });
      if (!res.ok) {
        setError(`Failed to remove the read-only token for ${entry.owner} (${res.status}).`);
        return;
      }
      await onChanged();
    } catch {
      setError("Could not reach the server. Please try again.");
    } finally {
      setRemoving(null);
    }
  };

  return (
    <section className="settings-section section">
      <p className="eyebrow">Workspaces</p>
      <h2 className="settings-heading section-title">Read-only tokens for workers</h2>
      <p className="settings-description">
        A cluster worker clones with one of these instead of your own write-capable credential.
        Add one token per GitHub or GitLab owner — your own account, or an organization you
        belong to — scoped to read-only access. Use a fine-grained token with Contents:
        read-only, limited to the repositories workers should clone. Blerg refuses classic
        tokens and checks GitLab scopes; it can't read a GitHub fine-grained token's
        permissions, so set them when you create it.
      </p>
      {error && (
        <p className="field-error" role="alert">
          {error}
        </p>
      )}
      <form className="card card-form" onSubmit={submit}>
        <label className="settings-field field" htmlFor="git-readonly-provider">
          <span className="field-label">Provider</span>
          <select
            className="settings-field"
            id="git-readonly-provider"
            value={provider}
            onChange={(e) => setProvider(e.target.value as Provider)}
          >
            {PROVIDERS.map((p) => (
              <option key={p} value={p}>
                {PROVIDER_LABEL[p]}
              </option>
            ))}
          </select>
        </label>
        <label className="settings-field field" htmlFor="git-readonly-owner">
          <span className="field-label">Owner</span>
          <input
            className="settings-field"
            id="git-readonly-owner"
            value={owner}
            onChange={(e) => setOwner(e.target.value)}
            placeholder="your-username or your-org"
            maxLength={64}
          />
        </label>
        <label className="settings-field field" htmlFor="git-readonly-token">
          <span className="field-label">Read-only token</span>
          <input
            type="password"
            className="settings-field"
            id="git-readonly-token"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder="Paste a read-only token…"
            autoComplete="off"
            spellCheck={false}
            autoCapitalize="off"
          />
        </label>
        <div className="card-actions">
          <button
            type="submit"
            className="settings-action btn btn-primary"
            disabled={submitting || owner.trim() === "" || token.trim() === ""}
          >
            {submitting ? "Saving…" : "Add token"}
          </button>
        </div>
      </form>
      {entries.length === 0 ? (
        <p className="muted">No read-only tokens yet.</p>
      ) : (
        <div className="table-wrap">
          <table className="token-table">
            <thead>
              <tr>
                <th scope="col">Owner</th>
                <th scope="col">Provider</th>
                <th scope="col">Last used</th>
                <th scope="col">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {entries.map((entry) => (
                <tr key={entry.engine}>
                  <td>{entry.owner}</td>
                  <td>{PROVIDER_LABEL[entry.provider]}</td>
                  <td>{formatLastUsed(entry.lastUsedAt)}</td>
                  <td>
                    <button
                      type="button"
                      className="settings-action btn btn-quiet"
                      onClick={() => remove(entry)}
                      disabled={removing === entry.engine}
                    >
                      {removing === entry.engine ? "Removing…" : "Remove"}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
