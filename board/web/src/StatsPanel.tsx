import { useEffect, useState } from "react";
import { api } from "./api";

interface Stats {
  wall_seconds?: number;
  agent_seconds?: Record<string, number>;
  sessions?: number;
  tokens?: { input: number; output: number; cache_read: number };
  gate?: { backend: string; calls: number; ms: number }[];
  models?: Record<string, string>;
}

function dur(secs: number): string {
  if (secs < 90) return `${secs}s`;
  if (secs < 5400) return `${Math.round(secs / 60)}m`;
  return `${(secs / 3600).toFixed(1)}h`;
}

function tok(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${Math.round(n / 1e3)}k`;
  return String(n);
}

// StatsPanel: what the card cost — wall clock, agent time (Claude) vs gate
// time (OpenAI-compatible backend), sessions, tokens.
export function StatsPanel({ cardId }: { cardId: string }) {
  const [stats, setStats] = useState<Stats | null>(null);
  useEffect(() => {
    api<Stats>(`/api/cards/${cardId}/stats`).then(setStats).catch(() => {});
  }, [cardId]);

  if (!stats) return null;
  const agent = Object.entries(stats.agent_seconds ?? {});
  const agentTotal = agent.reduce((a, [, s]) => a + s, 0);
  const gateMS = (stats.gate ?? []).reduce((a, g) => a + g.ms, 0);
  const t = stats.tokens;
  const models = Object.entries(stats.models ?? {});
  const hasAnything = stats.wall_seconds || agentTotal > 0 || gateMS > 0 ||
    (t && (t.input + t.output) > 0) || models.length > 0;
  if (!hasAnything) return null;

  return (
    <section>
      <h4>Stats</h4>
      <dl className="kv">
        {stats.wall_seconds != null && (
          <><dt>completed in</dt><dd>{dur(stats.wall_seconds)}</dd></>
        )}
        {agentTotal > 0 && (
          <>
            <dt>agent time</dt>
            <dd>
              {dur(agentTotal)}
              {stats.sessions ? ` across ${stats.sessions} session${stats.sessions === 1 ? "" : "s"}` : ""}
              {agent.length > 1 &&
                ` (${agent.map(([r, s]) => `${r} ${dur(s)}`).join(" · ")})`}
            </dd>
          </>
        )}
        {models.length > 0 && (
          <>
            <dt>model</dt>
            <dd>
              {new Set(models.map(([, m]) => m)).size === 1
                ? models[0][1]
                : models.map(([role, m]) => `${role} ${m}`).join(" · ")}
            </dd>
          </>
        )}
        {(stats.gate?.length ?? 0) > 0 && gateMS > 0 && (
          <>
            <dt>gate time</dt>
            <dd>
              {(gateMS / 1000).toFixed(1)}s
              {" ("}{stats.gate!.map((g) => `${g.backend} ×${g.calls}`).join(" · ")}{")"}
            </dd>
          </>
        )}
        {t && (t.input + t.output) > 0 && (
          <>
            <dt>tokens</dt>
            <dd>
              {tok(t.input + t.output)} ({tok(t.input)} in / {tok(t.output)} out
              {t.cache_read > 0 ? ` · ${tok(t.cache_read)} cached` : ""})
            </dd>
          </>
        )}
      </dl>
    </section>
  );
}
