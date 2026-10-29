import { useCallback, useEffect, useRef, useState } from "react";
import { apiFetch } from "./apiFetch";
import type { Site } from "./Shell";

// Settings → Account → Your desktops (#193). A desktop credential is account identity, issued by
// core, so desktops are managed here: GET /api/desktops lists the person's desktops (and, for an
// administrator, everyone's) from the runner; Rename, Remove and Enroll go back through core.
// "Add a desktop" creates a one-time code and shows the one command to run on the new machine:
// the code is shown once, here, and never again.

type Desktop = {
  id: string;
  name: string;
  host_name: string;
  owner_id: string;
  ownership_revision: number;
  state: "enrolled" | "offline" | "needs_enrolling";
  connected: boolean;
  last_seen_at: string | null;
  status_reason?: string;
  mine: boolean;
  can_enroll: boolean;
  can_rename: boolean;
  can_revoke: boolean;
};
type Owner = { id: string; name: string };
type View = { desktops: Desktop[]; owners: Owner[]; actor: string; can_manage_others: boolean };
type Code = { id: string; code?: string; expires_at: string };
type Connection = { desktop_id: string; connection_generation: string; hostname: string; os_user: string; connected_since: string; source_address: string };

const STATE_LABEL: Record<Desktop["state"], string> = { enrolled: "Enrolled", offline: "Offline", needs_enrolling: "Needs enrolling" };
const STATE_CHIP: Record<Desktop["state"], string> = { enrolled: "chip up", offline: "chip idle", needs_enrolling: "chip pending" };

async function errorText(res: Response, fallback: string): Promise<string> {
  try {
    const body = (await res.json()) as { error?: unknown };
    if (typeof body?.error === "string" && body.error) return body.error;
  } catch {
    // not JSON
  }
  return fallback;
}

function when(value: string | null): string {
  if (!value) return "never";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? "unknown" : d.toLocaleString();
}

// The daemon dials the runner's /ws/daemon; the command names it so a new machine needs nothing else.
export function daemonServerURL(site: Site | null): string {
  const base = site?.runner_url || `${location.protocol}//${location.hostname}:8083`;
  return base.replace(/^http/, "ws").replace(/\/+$/, "") + "/ws/daemon";
}

export function enrollCommand(site: Site | null, code: string): string {
  return `BLERG_RUNNER_SERVER_URL=${daemonServerURL(site)} install/desktop/daemon/install.sh install --enroll ${code}`;
}

type Loaded = { view?: View; codes?: Code[]; error?: string };

async function fetchDesktops(): Promise<Loaded> {
  try {
    const [desktops, live] = await Promise.all([apiFetch("/api/desktops"), apiFetch("/api/desktop-enrollment-codes")]);
    const out: Loaded = {};
    if (!desktops.ok) {
      out.error = await errorText(desktops, `Could not load your desktops (${desktops.status}).`);
    } else {
      const body = (await desktops.json()) as View;
      if (!Array.isArray(body?.desktops)) return { error: "Could not load your desktops." };
      out.view = { ...body, owners: Array.isArray(body.owners) ? body.owners : [] };
    }
    if (live.ok) {
      const body = (await live.json()) as { codes?: Code[] };
      out.codes = Array.isArray(body?.codes) ? body.codes : [];
    }
    return out;
  } catch {
    return { error: "Could not reach the server to load your desktops." };
  }
}

export default function DesktopsSection({ site }: { site: Site | null }) {
  const [view, setView] = useState<View | null>(null);
  const [codes, setCodes] = useState<Code[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");
  const [issued, setIssued] = useState<Code | null>(null);
  const [copied, setCopied] = useState(false);
  const [renaming, setRenaming] = useState<{ id: string; name: string } | null>(null);
  const [removing, setRemoving] = useState<string>("");
  const [confirming, setConfirming] = useState<Connection | null>(null);
  const [assignTo, setAssignTo] = useState<Record<string, string>>({});
  const sectionRef = useRef<HTMLElement>(null);

  const apply = useCallback((r: Loaded) => {
    if (r.view) {
      setView(r.view);
      setError("");
    } else if (r.error) setError(r.error);
    if (r.codes) setCodes(r.codes);
  }, []);
  const load = useCallback(async () => apply(await fetchDesktops()), [apply]);

  useEffect(() => {
    let cancelled = false;
    void fetchDesktops().then((r) => {
      if (!cancelled) apply(r);
    });
    return () => {
      cancelled = true;
    };
  }, [apply]);

  // Runner links here as /settings#desktops; the section is below Account.
  useEffect(() => {
    if (location.hash === "#desktops") sectionRef.current?.scrollIntoView?.({ block: "start" });
  }, [view]);

  const run = async (key: string, fn: () => Promise<string | null>) => {
    if (busy) return;
    setBusy(key);
    setError("");
    setNotice("");
    try {
      const failure = await fn();
      if (failure) setError(failure);
    } catch {
      setError("Could not reach the server. Your desktops were not changed.");
    } finally {
      setBusy("");
    }
  };

  const addDesktop = () =>
    run("add", async () => {
      const res = await apiFetch("/api/desktop-enrollment-codes", { method: "POST" });
      if (!res.ok) return errorText(res, `Could not create an enrollment code (${res.status}).`);
      const body = (await res.json()) as Code;
      setIssued(body);
      setCopied(false);
      setCodes((list) => [...list, { id: body.id, expires_at: body.expires_at }]);
      return null;
    });

  const revokeCode = (id: string) =>
    run("code:" + id, async () => {
      const res = await apiFetch(`/api/desktop-enrollment-codes/${encodeURIComponent(id)}`, { method: "DELETE" });
      if (!res.ok && res.status !== 404) return errorText(res, `Could not revoke the code (${res.status}).`);
      setCodes((list) => list.filter((c) => c.id !== id));
      if (issued?.id === id) setIssued(null);
      setNotice("Code revoked. It can no longer add a desktop.");
      return null;
    });

  const copy = async () => {
    if (!issued?.code) return;
    try {
      await navigator.clipboard.writeText(enrollCommand(site, issued.code));
      setCopied(true);
    } catch {
      setCopied(false);
    }
  };

  const rename = (d: Desktop) =>
    run("rename:" + d.id, async () => {
      const res = await apiFetch(`/api/desktops/${encodeURIComponent(d.id)}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: renaming?.name ?? "" }),
      });
      if (!res.ok) return errorText(res, `Could not rename the desktop (${res.status}).`);
      setRenaming(null);
      await load();
      return null;
    });

  const remove = (d: Desktop) =>
    run("remove:" + d.id, async () => {
      const res = await apiFetch(`/api/desktops/${encodeURIComponent(d.id)}`, { method: "DELETE" });
      if (!res.ok) return errorText(res, `Could not remove the desktop (${res.status}).`);
      setRemoving("");
      setNotice(`${d.name} was removed and its credential revoked. To use it again, add it again.`);
      await load();
      return null;
    });

  const present = (d: Desktop) =>
    run("enroll:" + d.id, async () => {
      const res = await apiFetch(`/api/desktops/${encodeURIComponent(d.id)}/enrollment-connection`);
      if (!res.ok) return errorText(res, "Connect this desktop and update its daemon, then select Enroll again.");
      const body = (await res.json()) as Connection;
      if (body.desktop_id !== d.id || !body.connection_generation || !body.hostname || !body.os_user || !body.connected_since || !body.source_address) {
        return "This desktop's connection details are unavailable. Update its daemon and try again.";
      }
      setConfirming(body);
      return null;
    });

  const confirm = () =>
    run("confirm", async () => {
      if (!confirming) return null;
      const res = await apiFetch(`/api/desktops/${encodeURIComponent(confirming.desktop_id)}/enroll`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ confirmed: true, connection_generation: confirming.connection_generation }),
      });
      setConfirming(null);
      if (!res.ok) return errorText(res, "Enrollment was refused or the connection changed. Select Enroll again and confirm the current details.");
      setNotice("Enrollment requested. The desktop reconnects with its own credential in a moment.");
      window.setTimeout(() => void load(), 3000);
      return null;
    });

  const assign = (d: Desktop) =>
    run("assign:" + d.id, async () => {
      const res = await apiFetch(`/api/desktops/${encodeURIComponent(d.id)}/owner`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ owner_id: assignTo[d.id], ownership_revision: d.ownership_revision }),
      });
      if (!res.ok) return errorText(res, `Could not assign the desktop (${res.status}).`);
      setNotice("Owner recorded. That person enrolls the desktop next.");
      await load();
      return null;
    });

  const ownerName = (id: string) => view?.owners.find((o) => o.id === id)?.name ?? "another person";
  const mine = view?.desktops.filter((d) => d.mine || (!d.owner_id && !view.can_manage_others)) ?? [];
  const others = view?.can_manage_others ? view.desktops.filter((d) => !d.mine) : [];

  const card = (d: Desktop) => (
    <li key={d.id} className="card desktop-card" data-testid={`desktop-${d.id}`}>
      <div className="card-head">
        <h3 className="settings-heading card-title desktop-name">{d.name}</h3>
        <span className={STATE_CHIP[d.state]}>
          <span className="dot" aria-hidden="true" />
          <span className="label">{STATE_LABEL[d.state]}</span>
        </span>
      </div>
      <p className="card-help desktop-meta">
        {d.connected ? "Connected now" : `Last seen ${when(d.last_seen_at)}`}
        {d.host_name && d.host_name !== d.name && <> · host <span className="mono">{d.host_name}</span></>}
        {!d.mine && d.owner_id && <> · {ownerName(d.owner_id)}’s desktop</>}
        {!d.owner_id && view?.can_manage_others && <> · unassigned</>}
      </p>
      {d.state === "needs_enrolling" && !d.can_enroll && (
        <p className="card-help">
          Connect this desktop with an updated daemon to enroll it here, or add it again with <strong>Add a desktop</strong>.
        </p>
      )}
      {renaming?.id === d.id ? (
        <form className="card-form" onSubmit={(e) => { e.preventDefault(); void rename(d); }}>
          <label className="settings-field field" htmlFor={`rename-${d.id}`}>
            <span className="field-label">Name</span>
            <input className="settings-field" id={`rename-${d.id}`} maxLength={64} value={renaming.name}
              onChange={(e) => setRenaming({ id: d.id, name: e.target.value })} autoFocus />
          </label>
          <div className="card-actions">
            <button type="submit" className="settings-action btn btn-primary" disabled={!!busy}>{busy === "rename:" + d.id ? "Saving…" : "Save name"}</button>
            <button type="button" className="settings-action btn btn-quiet" onClick={() => setRenaming(null)}>Cancel</button>
          </div>
        </form>
      ) : removing === d.id ? (
        <div className="desktop-confirm" role="group" aria-label={`Remove ${d.name}`}>
          <p className="card-help">
            Remove <strong>{d.name}</strong>? It disconnects now and its credential is revoked. To use it again, add it again.
          </p>
          <div className="card-actions">
            <button type="button" className="settings-action btn btn-danger" disabled={!!busy} onClick={() => void remove(d)}>{busy === "remove:" + d.id ? "Removing…" : "Remove desktop"}</button>
            <button type="button" className="settings-action btn btn-quiet" onClick={() => setRemoving("")}>Cancel</button>
          </div>
        </div>
      ) : (
        <div className="card-actions">
          {d.state === "needs_enrolling" && d.can_enroll && (
            <button type="button" className="settings-action btn btn-primary" disabled={!!busy} onClick={() => void present(d)}>
              {busy === "enroll:" + d.id ? "Loading connection…" : "Enroll"}
            </button>
          )}
          {d.can_rename && (
            <button type="button" className="settings-action btn" disabled={!!busy} onClick={() => setRenaming({ id: d.id, name: d.name })}>Rename</button>
          )}
          {d.can_revoke && (
            <button type="button" className="settings-action btn btn-quiet" disabled={!!busy} onClick={() => setRemoving(d.id)}>Remove</button>
          )}
          {view?.can_manage_others && !d.owner_id && (
            <>
              <label className="settings-field desktop-assign" htmlFor={`assign-${d.id}`}>
                <span className="sr-only">Owner of {d.name}</span>
                <select className="settings-field" id={`assign-${d.id}`} value={assignTo[d.id] ?? ""} onChange={(e) => setAssignTo({ ...assignTo, [d.id]: e.target.value })}>
                  <option value="">Choose a person</option>
                  {view.owners.map((o) => <option key={o.id} value={o.id}>{o.name}</option>)}
                </select>
              </label>
              <button type="button" className="settings-action btn" disabled={!!busy || !assignTo[d.id]} onClick={() => void assign(d)}>Assign</button>
            </>
          )}
        </div>
      )}
      {confirming?.desktop_id === d.id && (
        <section className="desktop-confirm" aria-label="Confirm desktop enrollment">
          <h4 className="desktop-confirm-title">Is this your desktop?</h4>
          <p className="card-help">Check that these details match the desktop you mean to enroll to you.</p>
          <dl className="desktop-details">
            <dt>Hostname (reported)</dt><dd><bdi>{confirming.hostname}</bdi></dd>
            <dt>OS user (reported)</dt><dd><bdi>{confirming.os_user}</bdi></dd>
            <dt>Connected since</dt><dd><time dateTime={confirming.connected_since}>{when(confirming.connected_since)}</time></dd>
            <dt>Source address seen by the server</dt><dd><bdi>{confirming.source_address}</bdi></dd>
          </dl>
          <div className="card-actions">
            <button type="button" className="settings-action btn btn-primary" disabled={!!busy} onClick={() => void confirm()}>{busy === "confirm" ? "Enrolling…" : "Confirm enrollment"}</button>
            <button type="button" className="settings-action btn btn-quiet" onClick={() => setConfirming(null)}>Cancel</button>
          </div>
        </section>
      )}
    </li>
  );

  return (
    <section className="settings-section section" id="desktops" ref={sectionRef} aria-labelledby="desktops-heading">
      <p className="eyebrow">Account</p>
      <h2 className="settings-heading section-title" id="desktops-heading">Your desktops</h2>
      <p className="settings-description">The machines that run your sessions. Each one is enrolled to you with its own credential.</p>
      <div className="card-actions desktop-toolbar">
        <button type="button" className="settings-action btn btn-primary" disabled={!!busy} onClick={() => void addDesktop()}>
          {busy === "add" ? "Creating code…" : "Add a desktop"}
        </button>
      </div>
      {issued?.code && (
        <div className="card token-reveal desktop-add" role="region" aria-label="Add a desktop">
          <div className="card-head">
            <h3 className="settings-heading card-title">Add a desktop</h3>
            <span className="chip active"><span className="dot" aria-hidden="true" /><span className="label">Shown once</span></span>
          </div>
          <p className="card-help">
            On the new machine, in a checkout of Blerg, run this command. The code works once and expires at{" "}
            <time dateTime={issued.expires_at}>{new Date(issued.expires_at).toLocaleTimeString()}</time>. Keep it private.
          </p>
          <pre className="token-curl mono" data-testid="enroll-command">{enrollCommand(site, issued.code)}</pre>
          <div className="card-actions">
            <button type="button" className="settings-action btn" onClick={() => void copy()}>Copy command</button>
            {copied && <span className="field-help" role="status">Copied</span>}
            <button type="button" className="settings-action btn btn-quiet" onClick={() => { setIssued(null); void load(); }}>Done</button>
            <button type="button" className="settings-action btn btn-quiet" disabled={!!busy} onClick={() => void revokeCode(issued.id)}>Cancel code</button>
          </div>
        </div>
      )}
      {error && <p className="field-error" role="alert">{error}</p>}
      {notice && <p className="field-help" role="status">{notice}</p>}
      {!view && !error && <p className="muted">Loading your desktops…</p>}
      {view && mine.length === 0 && (
        <p className="muted">No desktops yet. Select <strong>Add a desktop</strong> and run its command on the machine.</p>
      )}
      {mine.length > 0 && <ul className="card-list desktop-list">{mine.map(card)}</ul>}
      {codes.filter((c) => c.id !== issued?.id).length > 0 && (
        <ul className="desktop-codes">
          {codes.filter((c) => c.id !== issued?.id).map((c) => (
            <li key={c.id} className="field-help">
              An unused enrollment code expires at <time dateTime={c.expires_at}>{new Date(c.expires_at).toLocaleTimeString()}</time>.{" "}
              <button type="button" className="settings-action btn btn-quiet" disabled={!!busy} onClick={() => void revokeCode(c.id)}>Revoke code</button>
            </li>
          ))}
        </ul>
      )}
      {others.length > 0 && (
        <div className="group">
          <p className="eyebrow">Everyone’s desktops (administrator)</p>
          <ul className="card-list desktop-list">{others.map(card)}</ul>
        </div>
      )}
    </section>
  );
}
