import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, Card } from "./api";
import { Conversation } from "./Conversation";

// SessionPage: the mobile-first full-screen session view at
// /boards/:id/cards/:number/session — back navigates to the card URL.
export function SessionPage() {
  const { id: boardId, number } = useParams();
  const [card, setCard] = useState<Card | null>(null);

  useEffect(() => {
    if (!boardId || !number) return;
    api<Card[]>(`/api/boards/${boardId}/cards`)
      .then((cs) => setCard((cs ?? []).find((c) => c.number === Number(number)) ?? null))
      .catch(() => {});
  }, [boardId, number]);

  if (!boardId || !number) return null;
  return (
    <main style={{ maxWidth: 720 }}>
      <div className="row" style={{ marginBottom: 10 }}>
        <Link className="btn ghost small" to={`/boards/${boardId}/cards/${number}`}>
          ← card #{number}
        </Link>
        {card && <span className="title" style={{ color: "var(--chalk)" }}>{card.title}</span>}
      </div>
      {card ? <Conversation cardId={card.id} /> : <div className="empty">loading…</div>}
    </main>
  );
}
