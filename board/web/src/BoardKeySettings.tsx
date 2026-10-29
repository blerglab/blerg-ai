import { useState } from "react";
import { api, type Board } from "./api";
import { useBoardAdmin } from "./me";

export function BoardKeySettings({ board, onChanged }: { board: Board; onChanged?: () => void }) {
  const isAdmin = useBoardAdmin(board.id);
  const [key, setKey] = useState(board.key || board.suggested_key || "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return <section>
    <h3 className="settings-heading">Board key</h3>
    <p className="settings-description">{board.key ? `Cards use ${board.key}-N. Old keys keep working after a rename.` : "Confirm a suggested key for this board. Existing card numbers stay the same."}</p>
    {isAdmin ? <form className="stack" onSubmit={async e => {
      e.preventDefault(); setBusy(true); setError("");
      try { await api(`/api/boards/${board.id}`, { method: "PATCH", json: { key } }); onChanged?.(); }
      catch (err) { setError(err instanceof Error ? err.message : "Could not save the board key."); }
      finally { setBusy(false); }
    }}>
      <label htmlFor="settings-board-key">Key (2–5 uppercase letters or digits, starting with a letter)</label>
      <input className="settings-field" id="settings-board-key" value={key} pattern="[A-Z][A-Z0-9]{1,4}" minLength={2} maxLength={5} required onChange={e => setKey(e.target.value.toUpperCase())} />
      <button type="submit" className="settings-action btn small" disabled={busy || key === board.key}>{board.key ? "Save key" : "Confirm key"}</button>
      {error && <p role="alert">{error}</p>}
    </form> : <p className="settings-description">A board admin can {board.key ? "rename" : "confirm"} this key.</p>}
  </section>;
}
