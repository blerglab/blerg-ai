import { useEffect, useState } from "react";
import { api, ApiError, ResolveResult, Review } from "./api";
import { useMe } from "./me";

function backendLabel(b?: string | null): string {
  return b ?? "";
}

export function GateLog() {
  const me = useMe();
  const [held, setHeld] = useState<Review[]>([]);
  const [log, setLog] = useState<Review[]>([]);
  const [notice, setNotice] = useState<{ id: string; text: string; danger?: boolean } | null>(null);
  const load = () => {
    api<Review[]>("/api/reviews?state=held").then((r) => setHeld(r ?? [])).catch(() => {});
    api<Review[]>("/api/reviews?limit=100").then((r) => setLog(r ?? [])).catch(() => {});
  };
  useEffect(load, []);

  const resolve = async (id: string, decision: "approve" | "reject") => {
    setNotice(null);
    try {
      const res = await api<ResolveResult>(`/api/reviews/${id}/resolve`, { json: { decision } });
      if (res.note) setNotice({ id, text: res.note });
    } catch (e) {
      setNotice({ id, text: e instanceof ApiError ? e.message : "resolve failed", danger: true });
      return; // leave the row in place so the human can see what it was
    }
    load();
  };

  return (
    <main>
      <h1 className="page">Admission gate</h1>

      {notice && (
        <p role="alert" style={{ color: notice.danger ? "var(--danger)" : "var(--amber)", fontSize: 13, marginBottom: 12 }}>
          {notice.text}
        </p>
      )}

      {held.length > 0 && (
        <section style={{ marginBottom: 28 }}>
          <h4 className="sign" style={{ color: "var(--amber)", fontSize: 13 }}>
            Held — waiting on you ({held.length})
          </h4>
          <table className="table">
            <thead>
              <tr><th>Submitted</th><th>Op</th><th>Payload</th><th>Expires</th><th /></tr>
            </thead>
            <tbody>
              {held.map((r) => (
                <tr key={r.id}>
                  <td className="mono">{r.submitted_at.slice(0, 16)}</td>
                  <td>{r.operation}</td>
                  <td>{String(r.payload?.title ?? JSON.stringify(r.payload).slice(0, 60))}</td>
                  <td className="mono">{r.held_expires_at?.slice(0, 16)}</td>
                  <td>
                    {me?.is_admin ? (
                      <div className="row">
                        <button className="btn small" onClick={() => resolve(r.id, "approve")}>Approve</button>
                        <button className="btn ghost small" onClick={() => resolve(r.id, "reject")}>Reject</button>
                      </div>
                    ) : (
                      <span className="admin-only">admin only</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}

      {log.length === 0 ? (
        <div className="empty">
          <p className="sign">No gate activity yet</p>
          <p>When agents start filing cards on a gated board, every verdict lands here — including the refused ones.</p>
        </div>
      ) : (
        <table className="table">
          <thead>
            <tr><th>When</th><th>Op</th><th>Verdict</th><th>Policy</th><th>Reason</th><th>Backend</th><th>ms</th></tr>
          </thead>
          <tbody>
            {log.map((r) => (
              <tr key={r.id}>
                <td className="mono">{r.submitted_at.slice(5, 16)}</td>
                <td>{r.operation}</td>
                <td><span className={`verdict ${r.verdict ?? ""}`}>{r.verdict ?? "—"}</span></td>
                <td className="mono">{r.policy_applied}</td>
                <td style={{ maxWidth: 380 }}>{r.reason}</td>
                <td className="mono">{backendLabel(r.backend)}</td>
                <td className="mono">{r.latency_ms ?? ""}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </main>
  );
}
