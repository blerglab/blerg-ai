import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import { consumeAccessTokenFromFragment, getAccessToken, startTokenRenewal } from "./authClient";
import { clearRefreshAttempted } from "./refreshGuard";
import "./styles.css";

// Pick up an access token left in the URL fragment by GET /auth/refresh (or /auth/callback)
// before the app renders — see authClient.ts.
consumeAccessTokenFromFragment();
// A fresh token means the refresh actually worked — clear the guard so a
// later, unrelated 401 (e.g. after this token itself expires) can trigger
// another refresh instead of being silently swallowed by a stale guard.
if (getAccessToken()) clearRefreshAttempted();
// Keep the token fresh in the background so a request after ten minutes does not have to send the
// page away to renew it.
if (getAccessToken()) startTokenRenewal();

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
