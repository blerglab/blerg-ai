import { useEffect, useState } from "react";
import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom";
import Login from "./Login";
import Settings from "./Settings";
import ChangePassword from "./ChangePassword";
import Shell, { useSite, type Site } from "./Shell";
import { getAccessToken, redirectToRefresh } from "./authClient";
import { apiFetch } from "./apiFetch";
import { mustChangePassword } from "./claims";
import { markRefreshAttempted, refreshAlreadyAttempted } from "./refreshGuard";

// Me mirrors GET /api/me's response body (core/internal/api/me_handler.go). Kept minimal —
// only the fields Home actually renders.
type Me = {
  provider_subject: string;
  role: string;
};

// Tile is one component this install exposes. Board and runner URLs come from GET /api/site
// (BLERG_CORE_ORIGIN_AUDIENCES inverted server-side); when nothing is configured the
// desktop-compose shape is assumed: one host, one port each.
type Tile = { name: string; description: string; url: string };

function tilesFor(site: Site | null): Tile[] {
  const origin = `${location.protocol}//${location.hostname}`;
  return [
    {
      name: "Board",
      description: "Kanban board where agents draft cards and humans curate the work.",
      url: site?.board_url || `${origin}:8082`,
    },
    {
      name: "Runner",
      description: "Launches and watches coding-agent sessions end to end.",
      url: site?.runner_url || `${origin}:8083`,
    },
  ];
}

type Health = "checking" | "up" | "down";

// ComponentTile probes the component's /healthz from the browser (CORS, 2.5 s budget) and
// shows the result as a status chip — colour is never the only signal, the chip text names
// the state.
function ComponentTile({ tile }: { tile: Tile }) {
  const [health, setHealth] = useState<Health>("checking");
  useEffect(() => {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 2500);
    fetch(`${tile.url}/healthz`, { mode: "cors", cache: "no-store", signal: controller.signal })
      .then((res) => setHealth(res.ok ? "up" : "down"))
      .catch(() => setHealth("down"))
      .finally(() => clearTimeout(timeout));
    return () => {
      controller.abort();
      clearTimeout(timeout);
    };
  }, [tile.url]);
  const label = health === "checking" ? "Checking" : health === "up" ? "Up" : "Down";
  const chipClass = health === "checking" ? "idle" : health;
  return (
    <a className="tile" href={tile.url}>
      <div className="tile-head">
        <h3 className="tile-name">{tile.name}</h3>
        <span className={`chip ${chipClass}`}>
          <span className="dot" aria-hidden="true" />
          <span className="label">{label}</span>
        </span>
      </div>
      <p className="tile-desc">{tile.description}</p>
      <span className="tile-url">{tile.url}</span>
    </a>
  );
}

// Home is the signed-in landing: the components this install exposes, and the account. It
// lives at /app (and "/" redirects here server-side — core/internal/api/router.go).
//
// A signed-out visitor never sees this screen: with no token in memory the page is sent
// through GET /auth/refresh, which either comes back with a token (a live session) or lands
// on the login form. Nobody has to notice a "Sign in" link to find out they need one.
function Home() {
  // R7/I-7: a must-change-password account's token carries only "password.change" — the
  // server enforces that (nothing else works with such a token), this is purely so the user
  // lands on the right screen instead of a page full of silent 403s. The screen itself is a
  // separate component so its hooks are never skipped by this early return.
  if (mustChangePassword(getAccessToken())) {
    return <Navigate to="/change-password" replace />;
  }
  return <HomeScreen />;
}

function HomeScreen() {
  const signedIn = getAccessToken() !== null;
  const [loggingOut, setLoggingOut] = useState(false);
  const [me, setMe] = useState<Me | null>(null);
  // Signed out and the one refresh bounce already spent: nothing more to try. Read once, on
  // mount, before the effect below spends the attempt (the token in memory doesn't change
  // mid-session outside of a full page load).
  const [stuck] = useState(() => !signedIn && refreshAlreadyAttempted());
  const site = useSite(signedIn);

  // Signed out: go get a token. Guarded like every other refresh bounce (refreshGuard.ts)
  // so a refresh that keeps producing tokens this page then loses can't loop.
  useEffect(() => {
    if (signedIn || stuck) return;
    markRefreshAttempted();
    redirectToRefresh(`${location.origin}/app`);
  }, [signedIn, stuck]);

  // Identity is read from GET /api/me, never decoded from the access token client-side — the
  // token's claims (contracts/identity.Claims) carry only Sub, not provider_subject/role/etc,
  // and core is the sole authority on what those actually are for the caller's account.
  useEffect(() => {
    if (!signedIn) return;
    let cancelled = false;
    apiFetch("/api/me")
      .then((res) => (res.ok ? res.json() : null))
      .then((data) => {
        if (!cancelled) setMe(data);
      })
      .catch(() => {
        if (!cancelled) setMe(null);
      });
    return () => {
      cancelled = true;
    };
  }, [signedIn]);

  // "Log out this device" hits POST /auth/logout (core/internal/api/auth_handlers.go's
  // handleLogout), which revokes only the session named by this browser's own refresh cookie
  // and clears it. It's a same-origin cookie-bearing POST, so no Authorization header is
  // needed — the browser sends the refresh cookie automatically. A full-page load of /login
  // afterward is deliberate: it's the simplest way to drop the in-memory access token
  // (authClient.ts never exposes a clear()) and it lands on the sign-in form rather than
  // bouncing through a refresh that is guaranteed to fail.
  const logout = async () => {
    setLoggingOut(true);
    try {
      await fetch("/auth/logout", { method: "POST" });
    } finally {
      location.href = "/login";
    }
  };

  // "Log out everywhere" hits POST /auth/logout-all (handleLogoutAll), authenticated by the
  // caller's own bearer access token via apiFetch — it revokes every live session for this
  // account (RevokeAccountEverywhere), not just this device, and also clears this browser's
  // own refresh cookie.
  const logoutAll = async () => {
    setLoggingOut(true);
    try {
      await apiFetch("/auth/logout-all", { method: "POST" });
    } finally {
      location.href = "/login";
    }
  };

  if (!signedIn) {
    return (
      <main className="auth">
        <div className="auth-card auth-card-quiet">
          {stuck ? (
            <>
              <p>Couldn't sign you in automatically.</p>
              <p>
                <a className="btn" href="/login">
                  Go to sign in
                </a>
              </p>
            </>
          ) : (
            <p>Redirecting to sign in…</p>
          )}
        </div>
      </main>
    );
  }

  const account = (
    <div className="account">
      {me ? (
        <>
          <span className="account-name">{me.provider_subject}</span>
          <span className="chip active">
            <span className="dot" aria-hidden="true" />
            <span className="label">{me.role}</span>
          </span>
        </>
      ) : (
        <span className="account-name muted">You're signed in.</span>
      )}
      <button type="button" className="btn btn-quiet" onClick={logout} disabled={loggingOut}>
        {loggingOut ? "Logging out…" : "Log out"}
      </button>
    </div>
  );

  return (
    <Shell aside={account} site={site}>
      <section className="section">
        <p className="eyebrow">Installed here</p>
        <h2 className="section-title">Tooling</h2>
        <p className="section-help">Every component this install has enabled, with a live status check run from your browser.</p>
        <div className="tiles">
          {tilesFor(site).map((tile) => (
            <ComponentTile key={tile.name} tile={tile} />
          ))}
        </div>
      </section>

      <section className="section">
        <p className="eyebrow">You</p>
        <h2 className="section-title">Account</h2>
        <p className="section-help">{me ? <>Signed in as {me.provider_subject} ({me.role})</> : "Signed in."}</p>
        <ul className="action-list">
          <li>
            <a className="action" href="/settings">
              <span className="action-name">Settings</span>
              <span className="action-desc">Engine and git credentials for cluster-runtime sessions.</span>
            </a>
          </li>
          <li>
            <a className="action" href="/change-password">
              <span className="action-name">Change password</span>
              <span className="action-desc">Signs you out everywhere once it's changed.</span>
            </a>
          </li>
          <li>
            <button type="button" className="action" onClick={logoutAll} disabled={loggingOut}>
              <span className="action-name">{loggingOut ? "Logging out…" : "Log out everywhere"}</span>
              <span className="action-desc">Ends every session for this account, on every device.</span>
            </button>
          </li>
        </ul>
      </section>
    </Shell>
  );
}

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/app" element={<Home />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/change-password" element={<ChangePassword />} />
        <Route path="/login" element={<Login />} />
        <Route path="*" element={<Navigate to="/app" replace />} />
      </Routes>
    </BrowserRouter>
  );
}
