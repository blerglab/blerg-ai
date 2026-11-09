import { useEffect, useState } from "react";
import { Link, useParams, useLocation } from "react-router-dom";
import { api, Card } from "./api";
import { getAccessToken } from "./authClient";

// DiffPage: the in-app unified diff at /boards/:id/cards/:number/diff.
export function DiffPage() {
  const { id: boardId, number } = useParams();
  const { search } = useLocation();
  const [loaded, setLoaded] = useState(false);
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
        return fetch(`/api/cards/${c.id}/diff${search}`, { credentials: "same-origin", headers: {Authorization: `Bearer ${getAccessToken() ?? ""}`} })
          .then(async (r) => {
            const text = await r.text();
            if (!r.ok) {
              try { setError(JSON.parse(text).error ?? text); }
              catch { setError(text); }
              return;
            }
            setDiff(text); setLoaded(true);
          });
      })
      .catch((e) => setError(String(e)));
  }, [boardId, number, search]);

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
      {!error && !loaded && <div className="empty">fetching diff…</div>}
      {loaded && !diff && <p>No changes beyond the base branch.</p>}
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
