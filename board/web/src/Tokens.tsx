import { useEffect, useState } from "react";
import { api, Board, Token } from "./api";
import { useMe } from "./me";

export function Tokens() {
  const me = useMe();
  const [tokens, setTokens] = useState<Token[]>([]);
  const [boards, setBoards] = useState<Board[]>([]);
  const [label, setLabel] = useState("");
  const [boardId, setBoardId] = useState("");
  const [secret, setSecret] = useState("");
  const [error, setError] = useState("");

  const load = () => {
    api<Token[]>("/api/tokens").then((t) => setTokens(t ?? [])).catch(() => {});
    api<Board[]>("/api/boards").then((b) => setBoards(b ?? [])).catch(() => {});
  };
  useEffect(load, []);

  const mint = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!label.trim()) return;
    setError("");
    try {
      const capabilities = boardId
        ? ["card.read", "card.write", "column.write"]
        : ["card.read", "card.write", "column.write", "board.admin"];
      const out = await api<{ secret: string }>("/api/tokens", {
        json: { label: label.trim(), board_id: boardId || null, ttl_hours: 720, capabilities },
      });
      setSecret(out.secret);
      setLabel("");
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to mint token");
    }
  };

  const revoke = async (id: string) => {
    setError("");
    try {
      await api(`/api/tokens/${id}/revoke`, { method: "POST" });
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to revoke token");
    }
  };

  const state = (t: Token) => {
    if (t.revoked_at) return "revoked";
    if (t.expires_at && new Date(t.expires_at) < new Date()) return "expired";
    return "live";
  };

  // UI hint only — the server enforces board.admin on every /api/tokens
  // route regardless of what renders here (see auth.Principal.RequireAdmin),
  // so a member who reaches this page some other way still gets a 403.
  // Fails closed: `!me?.is_admin` (not `me && !me.is_admin`) hides the page
  // both while /api/me is still loading and if that fetch ever fails
  // permanently (MeProvider's .catch leaves `me` null forever) — a brief
  // flash of denial while loading is the same tradeoff every other gate in
  // this diff (App.tsx's Tokens nav link, CardSheet's Accept/Reject,
  // GateLog's Approve/Reject) already accepts, and is far better than a
  // fetch hiccup silently leaving the mint/revoke controls exposed.
  if (!me?.is_admin) {
    return <p className="empty">Only a board admin can mint tokens.</p>;
  }

  return (
    <main>
      <h1 className="page">Agent tokens</h1>
      <form className="stack" onSubmit={mint} style={{ marginBottom: 12 }}>
        <label className="field">
          Label — who is this credential? (shows up in every audit row)
          <input type="text" value={label} placeholder="blerg-runner nightly sweep"
            onChange={(e) => setLabel(e.target.value)} />
        </label>
        <label className="field">
          Board scope
          <select value={boardId} onChange={(e) => setBoardId(e.target.value)}>
            <option value="">all boards</option>
            {boards.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
          </select>
        </label>
        <button className="btn" type="submit">Mint token</button>
      </form>
      {error && <p className="form-error">{error}</p>}
      {secret && (
        <div className="secret" style={{ marginBottom: 20 }}>
          {secret}
          <div style={{ fontFamily: "var(--body)", color: "var(--amber)", marginTop: 6 }}>
            Shown once — set it as BLERG_BOARD_TOKEN now.
          </div>
        </div>
      )}
      <table className="table">
        <thead>
          <tr><th>Label</th><th>Kind</th><th>Scope</th><th>Last used</th><th>State</th><th /></tr>
        </thead>
        <tbody>
          {tokens.map((t) => (
            <tr key={t.id} style={state(t) !== "live" ? { opacity: 0.45 } : undefined}>
              <td>{t.label}</td>
              <td className="mono">{t.kind}</td>
              <td>{t.board_id ? (boards.find((b) => b.id === t.board_id)?.name ?? "…") : "all"}</td>
              <td className="mono">{t.last_used_at?.slice(0, 16) ?? "never"}</td>
              <td className="mono">{state(t)}</td>
              <td>
                {state(t) === "live" && (
                  <button className="btn ghost small" onClick={() => revoke(t.id)}>Revoke</button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </main>
  );
}
