import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, Card } from "./api";

// DiffPage: the in-app unified diff at /boards/:id/cards/:number/diff.
export function DiffPage() {
  const { id: boardId, number } = useParams();
  const [card, setCard] = useState<Card | null>(null);
  const [diff, setDiff] = useState<string>("");
  const [error, setError] = useState("");

  useEffect(() => {
    if (!boardId || !number) return;
    api<Card[]>(`/api/boards/${boardId}/cards`)
      .then((cs) => {
        const c = (cs ?? []).find((x) => x.number === Number(number)) ?? null;
        setCard(c);
        if (!c) return;
        return fetch(`/api/cards/${c.id}/diff`, { credentials: "same-origin" })
          .then(async (r) => {
            const text = await r.text();
            if (!r.ok) {
              try { setError(JSON.parse(text).error ?? text); }
              catch { setError(text); }
              return;
            }
            setDiff(text);
          });
      })
      .catch((e) => setError(String(e)));
  }, [boardId, number]);

  const lineClass = (l: string) =>
    l.startsWith("+++") || l.startsWith("---") ? "file"
      : l.startsWith("diff ") ? "filehead"
      : l.startsWith("@@") ? "hunk"
      : l.startsWith("+") ? "add"
      : l.startsWith("-") ? "del"
      : "ctx";

  return (
    <main style={{ maxWidth: 1100 }}>
      <div className="row" style={{ marginBottom: 10 }}>
        <Link className="btn ghost small" to={`/boards/${boardId}/cards/${number}`}>
          ← card #{number}
        </Link>
        {card && <span style={{ color: "var(--chalk)" }}>{card.title}</span>}
      </div>
      {error && <p role="alert" style={{ color: "var(--danger)" }}>{error}</p>}
      {!error && !diff && <div className="empty">fetching diff…</div>}
      {diff && (
        <pre className="diff">
          {diff.split("\n").map((l, i) => (
            <div className={`dl ${lineClass(l)}`} key={i}>{l || " "}</div>
          ))}
        </pre>
      )}
    </main>
  );
}
