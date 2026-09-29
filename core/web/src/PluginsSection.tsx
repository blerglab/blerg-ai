import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";

// Always-on plugins: GET/PUT /api/plugins/{engine} (core/internal/api/plugin_handlers.go), the
// ordered list of engine plugins installed into every cluster session started as this account.
// Not secret, so unlike credentials it is shown back. The list is replaced as a whole on every
// change (add, remove) and core validates it: a rejection comes back as a short plain-text
// reason (bad name, "marketplace not allowed by this install", too many), shown as text — capped,
// never as markup.
//
// Data-driven like the credential kinds in Settings.tsx: an engine that later registers plugins
// in core is one more entry in PLUGIN_ENGINES (suggestions included), not new JSX.

type PluginEntry = { marketplace: string; plugin: string };
type PluginList = {
  plugins: PluginEntry[];
  allowed_marketplaces: string[];
  any_marketplace: boolean;
  max: number;
};
type PluginSuggestion = PluginEntry & { blurb: string };
type PluginEngine = {
  engine: string;
  label: string;
  defaultMarketplace: string;
  suggestions: readonly PluginSuggestion[];
};

const OFFICIAL_MARKETPLACE = "anthropics/claude-plugins-official";

export const PLUGIN_ENGINES: readonly PluginEngine[] = [
  {
    engine: "claude",
    label: "Claude Code",
    defaultMarketplace: OFFICIAL_MARKETPLACE,
    suggestions: [
      {
        marketplace: OFFICIAL_MARKETPLACE,
        plugin: "frontend-design",
        blurb: "Distinctive, intentional UI design guidance",
      },
      {
        marketplace: OFFICIAL_MARKETPLACE,
        plugin: "superpowers",
        blurb: "Planning, debugging and development workflows",
      },
    ],
  },
];

const SESSION_EXPIRED = "Your session has expired. Reload the page to sign in again.";

const samePlugin = (a: PluginEntry, b: PluginEntry) =>
  a.marketplace.toLowerCase() === b.marketplace.toLowerCase() && a.plugin === b.plugin;

// pluginRejection is core's own words for a 422 (plain text). Only a 422 is trusted to carry a
// user-facing reason; anything else gets the generic message from the caller.
async function pluginRejection(res: Response): Promise<string | null> {
  if (res.status !== 422) return null;
  try {
    const text = (await res.text()).trim();
    return text === "" ? null : text.slice(0, 200);
  } catch {
    return null;
  }
}

// normalize tolerates a body that is not the documented shape (a proxy page, an older core): the
// section then shows an empty list rather than crashing the whole Settings page.
function normalize(raw: unknown): PluginList {
  const r = (raw ?? {}) as Partial<PluginList>;
  return {
    plugins: Array.isArray(r.plugins) ? r.plugins : [],
    allowed_marketplaces: Array.isArray(r.allowed_marketplaces) ? r.allowed_marketplaces : [],
    any_marketplace: r.any_marketplace === true,
    max: typeof r.max === "number" ? r.max : 20,
  };
}

function PluginEngineCard({ engine, label, defaultMarketplace, suggestions }: PluginEngine) {
  const [list, setList] = useState<PluginList | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [marketplace, setMarketplace] = useState(defaultMarketplace);
  const [plugin, setPlugin] = useState("");

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await apiFetch(`/api/plugins/${engine}`);
        if (cancelled) return;
        if (res.status === 401) setError(SESSION_EXPIRED);
        else if (!res.ok) setError(`Failed to load ${label} plugins (${res.status}).`);
        else setList(normalize(await res.json()));
      } catch {
        if (!cancelled) setError("Could not reach the server. Please try again.");
      }
      if (!cancelled) setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [engine, label]);

  // save replaces the whole list. Returns true when core accepted it.
  const save = async (next: PluginEntry[]): Promise<boolean> => {
    setSaving(true);
    setError(null);
    try {
      const res = await apiFetch(`/api/plugins/${engine}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ plugins: next }),
      });
      if (!res.ok) {
        const reason = await pluginRejection(res);
        setError(reason ?? `Failed to save ${label} plugins (${res.status}).`);
        return false;
      }
      setList(normalize(await res.json()));
      return true;
    } catch {
      setError("Could not reach the server. Please try again.");
      return false;
    } finally {
      setSaving(false);
    }
  };

  const current = list?.plugins ?? [];
  const has = (e: PluginEntry) => current.some((c) => samePlugin(c, e));
  const draft: PluginEntry = { marketplace: marketplace.trim(), plugin: plugin.trim() };

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!draft.marketplace || !draft.plugin || !list) return;
    if (await save([...current, draft])) setPlugin("");
  };

  return (
    <li className="card">
      <div className="card-head">
        <h3 className="card-title">{label}</h3>
        <span className={current.length > 0 ? "chip up" : "chip idle"}>
          <span className="dot" aria-hidden="true" />
          <span className="label">{current.length} always on</span>
        </span>
      </div>
      {loading && <p className="muted">Loading…</p>}
      {error && (
        <p className="field-error" role="alert">
          {error}
        </p>
      )}
      {list && (
        <>
          {current.length === 0 ? (
            <p className="muted">No always-on plugins yet.</p>
          ) : (
            <div className="table-wrap">
              <table className="token-table">
                <thead>
                  <tr>
                    <th scope="col">Plugin</th>
                    <th scope="col">Marketplace</th>
                    <th scope="col">
                      <span className="sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {current.map((p) => (
                    <tr key={`${p.marketplace}/${p.plugin}`}>
                      <td>{p.plugin}</td>
                      <td className="mono">{p.marketplace}</td>
                      <td>
                        <button
                          type="button"
                          className="btn btn-quiet"
                          disabled={saving}
                          aria-label={`Remove ${p.plugin}`}
                          onClick={() => void save(current.filter((c) => !samePlugin(c, p)))}
                        >
                          Remove
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          <p className="card-help">Suggestions:</p>
          <div className="card-actions">
            {suggestions.map((s) => (
              <button
                key={`${s.marketplace}/${s.plugin}`}
                type="button"
                className="btn"
                title={s.blurb}
                disabled={saving || has(s)}
                onClick={() => void save([...current, { marketplace: s.marketplace, plugin: s.plugin }])}
              >
                {has(s) ? `${s.plugin} added` : `Add ${s.plugin}`}
              </button>
            ))}
          </div>

          <form className="card-form" onSubmit={add}>
            <label className="field" htmlFor={`plugin-marketplace-${engine}`}>
              <span className="field-label">Marketplace (GitHub owner/repo)</span>
              <input
                id={`plugin-marketplace-${engine}`}
                className="mono"
                value={marketplace}
                onChange={(e) => setMarketplace(e.target.value)}
                maxLength={140}
              />
            </label>
            <label className="field" htmlFor={`plugin-name-${engine}`}>
              <span className="field-label">Plugin name</span>
              <input
                id={`plugin-name-${engine}`}
                value={plugin}
                onChange={(e) => setPlugin(e.target.value)}
                placeholder="superpowers"
                maxLength={64}
              />
            </label>
            <div className="card-actions">
              <button
                type="submit"
                className="btn btn-primary"
                disabled={saving || draft.marketplace === "" || draft.plugin === ""}
              >
                {saving ? "Saving…" : "Add plugin"}
              </button>
            </div>
          </form>
          <p className="field-help">
            {list.any_marketplace
              ? "This install accepts any GitHub marketplace."
              : `This install accepts these marketplaces: ${list.allowed_marketplaces.join(", ") || "none"}.`}{" "}
            Up to {list.max} plugins.
          </p>
        </>
      )}
    </li>
  );
}

export default function PluginsSection() {
  return (
    <section className="section">
      <p className="eyebrow">Sessions</p>
      <h2 className="section-title">Always-on plugins</h2>
      <p className="section-help">
        These plugins are installed at the start of every cluster session started as you,
        including sessions your boards start with your automation token. They do not change
        Claude Code on your own machine. Plugins run code in your sessions, so only add ones you
        trust.
      </p>
      <ul className="card-list">
        {PLUGIN_ENGINES.map((e) => (
          <PluginEngineCard key={e.engine} {...e} />
        ))}
      </ul>
    </section>
  );
}
