import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import { consumeAccessTokenFromFragment, getAccessToken, startTokenRenewal } from './authClient'
import { clearRefreshAttempted } from './refreshGuard'

// Pick up an access token left in the URL fragment by GET /auth/refresh
// before the app renders — see authClient.ts.
consumeAccessTokenFromFragment()
// A token actually landed in memory: this page load's refresh (if any) truly
// succeeded, so clear the once-per-load guard. Otherwise leave it as-is —
// ws.ts's own module-load connect() (imported by App below) may have already
// set it, and if the fragment carried nothing we're still in the same
// "no token yet" state that guard is protecting.
if (getAccessToken()) clearRefreshAttempted()
// Keep the token fresh in the background so a request after ten minutes does not have to send the
// page away to renew it.
if (getAccessToken()) startTokenRenewal()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
