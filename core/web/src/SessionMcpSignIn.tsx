import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";

type Preview = {
  draft_id: string;
  runner_session_id: string;
  name: string;
  source: "settings" | "session_suggestion_saved_by_human";
  resource_host: string;
  issuer_host: string;
  provider_host: string;
  scopes: string[];
};
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const unavailable = "This sign-in result is no longer available. Return to your session to try again.";
const failed = "Sign-in could not be completed. Return to your session to try again.";
const warned = new Set<string>();
function warn(cause: "status_request" | "status_response" | "review_request" | "review_response" | "start_request" | "start_response" | "start_destination" | "cancel_request" | "cancel_response") {
  // Fixed vocabulary, once per cause per page. Never include provider data or URLs.
  if (!warned.has(cause)) { warned.add(cause); console.warn(`mcp_session_sign_in:${cause}`); }
}
const signedIn = "Signed in for this session. Select tools in your session to use them.";

function requestFromLocation() {
  const query = new URLSearchParams(window.location.search);
  const session = query.get("mcp_runner_session") ?? "";
  const definition = query.get("mcp_definition") ?? "";
  const result = query.get("mcp_session_result") ?? "";
  return { session: uuid.test(session) ? session : "", definition: uuid.test(definition) ? definition : "", result: uuid.test(result) ? result : "", error: query.has("mcp_session_error") };
}
function previewFrom(body: unknown): Preview | null {
  if (!body || typeof body !== "object") return null;
  const p = body as Partial<Preview>;
  if (typeof p.draft_id !== "string" || !uuid.test(p.draft_id) || typeof p.name !== "string" || typeof p.resource_host !== "string" || typeof p.issuer_host !== "string" || typeof p.provider_host !== "string" || !Array.isArray(p.scopes) || !p.scopes.every(scope => typeof scope === "string") || (p.source !== "settings" && p.source !== "session_suggestion_saved_by_human")) return null;
  return p as Preview;
}

// Identifiers may link here from Runner. Resource URLs, names and scope fields
// are never prefilled from a suggestion or query. Core resolves their provenance.
export default function SessionMcpSignIn() {
  const [request] = useState(requestFromLocation);
  const [scopes, setScopes] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState(request.error ? failed : "");
  const [error, setError] = useState("");

  useEffect(() => {
    if (!request.result) return;
    let canceled = false;
    void (async () => {
      try {
        const response = await apiFetch(`/api/mcp/session-oauth/${request.result}`);
        if (!response.ok) { warn("status_response"); if (!canceled) setError(unavailable); return; }
        const status = await response.json() as { stage?: unknown };
        if (!canceled) {
          if (status.stage === "completed") setMessage(signedIn);
          else if (status.stage === "pending" || status.stage === "starting") setMessage("Sign-in is still pending.");
          else { warn("status_response"); setError(unavailable); }
        }
      } catch { warn("status_request"); if (!canceled) setError(unavailable); }
    })();
    return () => { canceled = true; };
  }, [request]);

  async function review() {
    if (busy) return;
    setBusy(true); setError("");
    try {
      const response = await apiFetch("/api/mcp/session-oauth/preview", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ runner_session_id: request.session, definition_id: request.definition, scopes }) });
      if (!response.ok) { warn("review_response"); setError("Remote MCP sign-in is not available. Try again from your session."); return; }
      const received = previewFrom(await response.json());
      if (!received) { warn("review_response"); setError(failed); return; }
      setPreview(received);
    } catch { warn("review_request"); setError(failed); }
    finally { setBusy(false); }
  }
  async function start() {
    if (!preview || busy) return;
    setBusy(true); setError("");
    try {
      const response = await apiFetch(`/api/mcp/session-oauth/${preview.draft_id}/start`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
      if (!response.ok) { warn("start_response"); setError(failed); return; }
      const body = await response.json() as { authorization_url?: unknown; authorization_server?: unknown };
      if (typeof body.authorization_url !== "string") { warn("start_response"); setError(failed); return; }
      const target = new URL(body.authorization_url);
      const expectedHost = new URL(`https://${preview.provider_host}`).host;
      if (target.protocol !== "https:" || target.username || target.password || target.hash || target.host.toLowerCase() !== expectedHost.toLowerCase() || body.authorization_server !== preview.provider_host) { warn("start_destination"); setError(failed); return; }
      window.location.assign(target.toString());
    } catch { warn("start_request"); setError(failed); }
    finally { setBusy(false); }
  }
  async function cancel() {
    if (!preview || busy) return;
    setBusy(true); setError("");
    try {
      const response = await apiFetch(`/api/mcp/session-oauth/${preview.draft_id}/cancel`, { method: "POST" });
      if (!response.ok) { warn("cancel_response"); setError(failed); return; }
      setPreview(null); setMessage("Sign-in was canceled.");
    } catch { warn("cancel_request"); setError(failed); }
    finally { setBusy(false); }
  }

  if (!request.result && !request.error && !(request.session && request.definition)) return null;
  return <div className="card" role="group" aria-label="Session MCP sign-in">
    <h3 className="settings-heading card-title">Session sign-in</h3>
    {message && <p className="field-help" role="status">{message}</p>}
    {error && <p className="field-error" role="alert">{error}</p>}
    {request.result && !message && !error && <p className="muted">Checking session sign-in…</p>}
    {request.session && request.definition && !preview && <>
      <p className="field-help">Review the server and provider before signing this session in.</p>
      <label className="settings-field field" htmlFor="session-mcp-scopes">
        <span className="field-label">Scopes for this session (optional)</span>
        <input className="settings-field mono" id="session-mcp-scopes" value={scopes} onChange={event => setScopes(event.target.value)} maxLength={1024} autoComplete="off" />
      </label>
      <div className="card-actions"><button type="button" className="settings-action btn btn-primary" disabled={busy} onClick={() => void review()}>{busy ? "Reviewing…" : "Review session sign-in"}</button></div>
    </>}
    {preview && <>
      <p className="field-help">{preview.source === "settings" ? "From settings" : "Suggested by this session"}</p>
      <p className="card-help">{preview.name}</p>
      <dl>
        <dt className="field-label">Server</dt><dd className="mono">{preview.resource_host}</dd>
        <dt className="field-label">Issuer</dt><dd className="mono">{preview.issuer_host}</dd>
        <dt className="field-label">Sign-in provider</dt><dd className="mono">{preview.provider_host}</dd>
        <dt className="field-label">Requested scopes</dt><dd>{preview.scopes.length ? preview.scopes.join(", ") : "No scopes requested; provider defaults may apply."}</dd>
      </dl>
      <div className="card-actions">
        <button type="button" className="settings-action btn btn-primary" disabled={busy} onClick={() => void start()}>{busy ? "Continuing…" : `Continue to ${preview.provider_host}`}</button>
        <button type="button" className="settings-action btn btn-quiet" disabled={busy} onClick={() => void cancel()}>Cancel sign-in</button>
      </div>
    </>}
  </div>;
}
