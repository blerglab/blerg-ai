import { useState } from "react";
import { api, type Card } from "./api";

export function EstimateEditor({ card, onChange }: { card: Card; onChange: (card: Card) => void }) {
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  async function save(field: string, value: string) {
    setSaving(true); setError("");
    try {
      onChange(await api<Card>(`/api/cards/${card.id}`, { method: "PATCH", body: JSON.stringify({ [field]: value, if_match: card.version }) }));
    } catch (err) { setError(String(err)); }
    finally { setSaving(false); }
  }
  return <div className="estimate-editor">
    {(["size", "risk", "complexity"] as const).map(field => <label key={field}>
      {field === "size" ? "Size" : field === "risk" ? "Risk" : "Complexity"}
      <select aria-label={field === "size" ? "Size" : field === "risk" ? "Risk" : "Complexity"}
        disabled={saving} value={card[field] ?? ""} onChange={e => void save(field, e.target.value)}>
        <option value="">Not estimated</option>
        {field === "size" && card.size === "XS" && <option value="XS" disabled>XS (legacy)</option>}
        {(field === "size" ? ["S", "M", "L", "XL"] : ["low", "medium", "high"]).map(value => <option key={value}>{value}</option>)}
      </select>
    </label>)}
    {error && <p role="alert">{error}</p>}
  </div>;
}
