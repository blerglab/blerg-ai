import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";

// Board call limits: GET/PUT /api/settings/board-limits (core/internal/api/board_limits_handlers.go),
// an administrator's setting (account.manage; the server refuses anyone else). How much board traffic
// one session's board calls may make through the runner, and how many live exchange tokens one
// account may hold. Core's live-token cap applies at the next exchange; runners read the rest within
// runner_refresh_seconds. A rejection comes back as a short plain-text reason, shown as text.

type Limits = {
  calls_per_minute: number;
  calls_in_flight: number;
  unrevoked_per_account: number;
  live_exchange_tokens: number;
};
type LimitsView = Limits & {
  defaults: Limits;
  ranges: Record<keyof Limits, [number, number]>;
  custom: boolean;
  updated_at: string | null;
  runner_refresh_seconds: number;
};

export const LIMIT_FIELDS: {
  key: keyof Limits;
  label: string;
  help: string;
}[] = [
  {
    key: "calls_per_minute",
    label: "Board requests a minute, per session",
    help: "A rolling minute; a refused request says when to retry.",
  },
  {
    key: "calls_in_flight",
    label: "Board calls at once, per session",
    help: "Each call holds one of the account's live exchange tokens while it runs.",
  },
  {
    key: "unrevoked_per_account",
    label: "Unrevoked credentials per account",
    help: "While revokes fail, an account stops starting board calls at this many credentials still live.",
  },
  {
    key: "live_exchange_tokens",
    label: "Live exchange tokens per account",
    help: "Core's cap, shared with every board connection of the account.",
  },
];

async function plainError(res: Response): Promise<string> {
  try {
    const text = (await res.text()).trim();
    if (text && !text.startsWith("<")) return text.slice(0, 200);
  } catch {
    // fall through
  }
  return `Could not save the limits (${res.status}).`;
}

/** "At 8 calls at once, 12 busy sessions of one account use all 100 live tokens": calls at once are
 *  per session, live tokens per account, so busy sessions × calls at once must stay below it. null
 *  while a value is not a positive number. */
export function headroomLine(
  inFlight: number,
  liveTokens: number,
): string | null {
  if (!(inFlight >= 1) || !(liveTokens >= 1)) return null;
  const sessions = Math.max(1, Math.ceil(liveTokens / inFlight));
  return `Calls at once count against the account's live exchange tokens: at ${inFlight} calls at once, ${sessions} busy ${
    sessions === 1 ? "session" : "sessions"
  } of one account use all ${liveTokens}, and its other board calls and launch-board connections wait until calls finish.`;
}

export default function BoardLimitsSection() {
  const [view, setView] = useState<LimitsView | null>(null);
  const [draft, setDraft] = useState<Record<keyof Limits, string> | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  const take = (v: LimitsView) => {
    setView(v);
    setDraft({
      calls_per_minute: String(v.calls_per_minute),
      calls_in_flight: String(v.calls_in_flight),
      unrevoked_per_account: String(v.unrevoked_per_account),
      live_exchange_tokens: String(v.live_exchange_tokens),
    });
  };

  useEffect(() => {
    let cancelled = false;
    apiFetch("/api/settings/board-limits")
      .then(async (res) => {
        if (cancelled) return;
        if (!res.ok) {
          setError(`Could not load the board call limits (${res.status}).`);
          return;
        }
        take((await res.json()) as LimitsView);
      })
      .catch(() => {
        if (!cancelled)
          setError("Could not reach the server. Please try again.");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!draft) return;
    setSaving(true);
    setError(null);
    setSaved(false);
    const body = Object.fromEntries(
      LIMIT_FIELDS.map((f) => [f.key, Number(draft[f.key])]),
    );
    try {
      const res = await apiFetch("/api/settings/board-limits", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) {
        setError(await plainError(res));
        return;
      }
      take((await res.json()) as LimitsView);
      setSaved(true);
    } catch {
      setError("Could not reach the server. Please try again.");
    } finally {
      setSaving(false);
    }
  };

  const reset = () => {
    if (!view) return;
    setDraft({
      calls_per_minute: String(view.defaults.calls_per_minute),
      calls_in_flight: String(view.defaults.calls_in_flight),
      unrevoked_per_account: String(view.defaults.unrevoked_per_account),
      live_exchange_tokens: String(view.defaults.live_exchange_tokens),
    });
    setSaved(false);
  };

  return (
    <section className="section" data-testid="board-limits">
      <p className="eyebrow">Administration</p>
      <h2 className="section-title">Board call limits</h2>
      <p className="section-help">
        How much board traffic a session's board calls may make, for every
        account on this install. Core's live-token cap applies at the next
        exchange; runners use the other limits within{" "}
        {view ? `${view.runner_refresh_seconds} seconds` : "a minute"}, at each
        session's next board call.
      </p>
      {error && (
        <p className="field-error" role="alert">
          {error}
        </p>
      )}
      {view && draft && (
        <form className="card card-form" onSubmit={submit}>
          {LIMIT_FIELDS.map((f) => {
            const [min, max] = view.ranges[f.key];
            const id = `board-limit-${f.key}`;
            return (
              <div key={f.key}>
                <label className="field" htmlFor={id}>
                  <span className="field-label">{f.label}</span>
                  <input
                    id={id}
                    type="number"
                    inputMode="numeric"
                    min={min}
                    max={max}
                    step={1}
                    required
                    value={draft[f.key]}
                    onChange={(e) => {
                      setDraft({ ...draft, [f.key]: e.target.value });
                      setSaved(false);
                    }}
                    aria-describedby={`${id}-help`}
                  />
                </label>
                <p className="field-help" id={`${id}-help`}>
                  {f.help} Default {view.defaults[f.key]}; {min} to {max}.
                </p>
              </div>
            );
          })}
          {headroomLine(
            Number(draft.calls_in_flight),
            Number(draft.live_exchange_tokens),
          ) && (
            <p className="field-help" data-testid="board-limits-headroom">
              {headroomLine(
                Number(draft.calls_in_flight),
                Number(draft.live_exchange_tokens),
              )}
            </p>
          )}
          {saved && (
            <p className="field-help" role="status">
              Saved. Runners use the new limits within{" "}
              {view.runner_refresh_seconds} seconds.
            </p>
          )}
          <div className="card-actions">
            <button
              type="button"
              className="btn"
              onClick={reset}
              disabled={saving}
            >
              Use the defaults
            </button>
            <button type="submit" className="btn btn-primary" disabled={saving}>
              {saving ? "Saving…" : "Save limits"}
            </button>
          </div>
        </form>
      )}
    </section>
  );
}
