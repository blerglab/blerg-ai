import { useEffect, useRef, useState } from "react";
import { apiFetch } from "./apiFetch";

export type ManagedCredentialSummary = {
  engine: string;
  updated_at: string;
  kind?: string;
  state?: string;
  expires_at?: string;
};
type Capability = { engine: string; kind: string; login: true; verification_origin: string };
type Attempt = {
  attempt_id: string;
  state: "pending" | "connected" | "reconnect_required";
  verification_url?: string;
  user_code?: string;
  expires_at?: string;
  interval_seconds?: number;
};
const FAILURE = "Could not complete the connection. Please try again.";
const STATES: Record<string, string> = {
  usable: "Connected", refresh_pending: "Refreshing", reconnect_required: "Reconnect required",
  configured: "Configured",
};

async function readManaged(url: string, controller: AbortController, init: RequestInit = {}): Promise<unknown> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      (async () => {
        const response = await apiFetch(url, { ...init, signal: controller.signal });
        if (!response.ok) throw new Error(FAILURE);
        return response.status === 204 ? null : response.json();
      })(),
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => { controller.abort(); reject(new Error(FAILURE)); }, 12_000);
      }),
    ]);
  } finally { if (timer !== undefined) clearTimeout(timer); }
}

async function refreshManaged(onRefresh: () => void | Promise<void>): Promise<boolean> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    await Promise.race([
      Promise.resolve().then(onRefresh),
      new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error(FAILURE)), 12_000); }),
    ]);
    return true;
  } catch { return false; }
  finally { if (timer !== undefined) clearTimeout(timer); }
}

function trustedLink(value: unknown, origin: string): string | null {
  if (typeof value !== "string") return null;
  try {
    const url = new URL(value);
    return url.protocol === "https:" && url.origin === origin && !url.username && !url.password && !url.hash
      ? url.href : null;
  } catch { return null; }
}

function attemptFrom(value: unknown, cap: Capability): Attempt | null {
  if (!value || typeof value !== "object") return null;
  const p = value as Partial<Attempt>;
  if (typeof p.attempt_id !== "string" || !/^[a-zA-Z0-9_-]{1,128}$/.test(p.attempt_id) ||
      !["pending", "connected", "reconnect_required"].includes(p.state ?? "")) return null;
  if (p.state === "pending" && (!trustedLink(p.verification_url, cap.verification_origin) ||
      typeof p.user_code !== "string" || p.user_code.length === 0 || p.user_code.length > 128 ||
      typeof p.expires_at !== "string" || !Number.isFinite(Date.parse(p.expires_at)))) return null;
  return {
    ...p,
    interval_seconds: typeof p.interval_seconds === "number" && Number.isFinite(p.interval_seconds)
      ? Math.max(1, Math.min(30, p.interval_seconds)) : 5,
  } as Attempt;
}

function Connection({ capability, summary, label, onConnected }: {
  capability: Capability;
  summary?: ManagedCredentialSummary;
  label: string;
  onConnected: () => void | Promise<void>;
}) {
  const [attempt, setAttempt] = useState<Attempt | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const request = useRef<AbortController | null>(null);
  const revision = useRef(0);
  const complete = useRef(onConnected);
  useEffect(() => { complete.current = onConnected; }, [onConnected]);
  const route = `/api/credentials/${encodeURIComponent(capability.engine)}/managed/login`;
  useEffect(() => () => { revision.current++; request.current?.abort(); }, []);

  async function start() {
    const current = ++revision.current;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setBusy(true); setError(null); setAttempt(null);
    try {
      const next = attemptFrom(await readManaged(route, controller, { method: "POST" }), capability);
      if (!next) throw new Error(FAILURE);
      if (revision.current !== current) return;
      setAttempt(next);
      if (next.state === "connected") {
        const refreshed = await refreshManaged(complete.current);
        if (revision.current === current) {
          if (refreshed) setAttempt(null);
          else setError("Connection updated, but its status could not be refreshed. Reload Settings.");
        }
      }
    } catch {
      if (revision.current === current) setError(FAILURE);
    } finally {
      if (revision.current === current) setBusy(false);
    }
  }

  async function cancel() {
    const current = ++revision.current;
    request.current?.abort();
    const controller = new AbortController(); request.current = controller;
    const pending = attempt;
    setBusy(true); setError(null);
    try {
      if (pending) {
        await readManaged(`${route}/${encodeURIComponent(pending.attempt_id)}`, controller, { method: "DELETE" });
      }
      if (revision.current === current) {
        setAttempt(null);
        if (!await refreshManaged(complete.current) && revision.current === current)
          setError("Connection updated, but its status could not be refreshed. Reload Settings.");
      }
    } catch { if (revision.current === current) setError("Could not cancel the connection. Try again."); }
    finally { if (revision.current === current) setBusy(false); }
  }

  useEffect(() => {
    if (attempt?.state !== "pending" || busy) return;
    const current = revision.current;
    let disposed = false;
    const controller = new AbortController(); request.current = controller;
    const remaining = Date.parse(attempt.expires_at!) - Date.now();
    const delay = Math.min(Math.max(1, Math.min(30, attempt.interval_seconds ?? 5)) * 1000, Math.max(0, remaining));
    const timer = setTimeout(async () => {
      if (remaining <= delay) {
        if (revision.current === current) { setAttempt(null); setError("Connection expired. Start again."); }
        return;
      }
      try {
        const next = attemptFrom(await readManaged(`${route}/${encodeURIComponent(attempt.attempt_id)}`, controller), capability);
        if (!next || next.attempt_id !== attempt.attempt_id) throw new Error(FAILURE);
        if (revision.current !== current || controller.signal.aborted) return;
        setAttempt(next);
        if (next.state === "connected") {
          const refreshed = await refreshManaged(complete.current);
          if (revision.current === current) {
            if (refreshed) setAttempt(null);
            else setError("Connection updated, but its status could not be refreshed. Reload Settings.");
          }
        }
      } catch {
        if (revision.current === current && !disposed) { setAttempt(null); setError(FAILURE); }
      }
    }, delay);
    return () => { disposed = true; clearTimeout(timer); controller.abort(); };
  }, [attempt, busy, capability, route]);

  const pending = attempt?.state === "pending";
  const state = attempt?.state === "connected" ? "Connected"
    : attempt?.state === "reconnect_required" ? "Reconnect required"
    : summary?.kind === capability.kind ? (STATES[summary.state ?? ""] ?? "Configured") : "Not connected";
  return <div className="card" aria-label={`${label} managed connection`}>
    <p className="card-help">{label}: {state}</p>
    <p className="card-help">Use a separate connection for Blerg’s Cluster sessions. Blerg renews it centrally; your machine’s login stays separate.</p>
    {error && <p className="field-error" role="alert">{error}</p>}
    {pending ? <>
      <p>Open <a href={trustedLink(attempt.verification_url, capability.verification_origin)!} target="_blank" rel="noopener noreferrer">the sign-in page</a> and enter <strong className="mono">{attempt.user_code}</strong>.</p>
      <p role="status">Waiting for sign-in…</p>
      <button className="settings-action btn btn-quiet" disabled={busy} onClick={() => void cancel()}>Cancel connection</button>
    </> : <button className="settings-action btn btn-quiet" disabled={busy} onClick={() => void start()}>
      {busy ? "Connecting…" : `${state === "Not connected" ? "Connect" : "Reconnect"} ${label}`}
    </button>}
  </div>;
}

export default function ManagedCredentials({ summaries, labels, onConnected }: {
  summaries: ManagedCredentialSummary[];
  labels: Record<string, string>;
  onConnected: () => void | Promise<void>;
}) {
  const [capabilities, setCapabilities] = useState<Capability[]>([]);
  useEffect(() => {
    const controller = new AbortController();
    void readManaged("/api/credentials/managed/capabilities", controller)
      .then((rows: unknown) => {
        if (controller.signal.aborted || !Array.isArray(rows)) return;
        const seen = new Set<string>();
        setCapabilities(rows.slice(0, 16).filter((r): r is Capability => {
          if (!r || typeof r !== "object" || typeof r.engine !== "string" ||
              !/^[a-z][a-z0-9_-]{0,31}$/.test(r.engine) || typeof r.kind !== "string" ||
              r.login !== true || !trustedLink(r.verification_origin, r.verification_origin) || seen.has(r.engine)) return false;
          seen.add(r.engine); return true;
        }));
      }).catch(() => {});
    return () => controller.abort();
  }, []);
  if (!capabilities.length) return null;
  return <div className="group" id="managed-connections" aria-label="Managed engine connections">
    <p className="eyebrow">Managed connections</p>
    {capabilities.map(capability => <Connection key={capability.engine} capability={capability}
      summary={summaries.find(s => s.engine === capability.engine)} label={labels[capability.engine] ?? capability.engine}
      onConnected={onConnected} />)}
  </div>;
}
