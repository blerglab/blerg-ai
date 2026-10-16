// signIn.ts — the one place this page decides to bounce to core's /auth/refresh for a token, and
// the app-level "couldn't sign you in" state when it may not (refreshGuard.ts says a bounce was
// already spent). Without that state a tab with no token showed an empty page and said nothing.
// Kept textually identical in runner/frontend/src/signIn.ts and board/web/src/signIn.ts.
import { useSyncExternalStore } from "react";
import { redirectToRefresh } from "./authClient";
import {
  markRefreshAttempted,
  refreshAlreadyAttempted,
  refreshReturnUrl,
  resetRefreshGuard,
} from "./refreshGuard";

/** How long after load a page with no token (and, in the runner, no socket) is taken to be stuck. */
export const SIGN_IN_GRACE_MS = 10_000;

let stuck = false;
const listeners = new Set<() => void>();

function setStuck(v: boolean): void {
  if (stuck === v) return;
  stuck = v;
  listeners.forEach((l) => l());
}

/** markSignInStuck: no token can be obtained without the person's say-so; show the banner. */
export function markSignInStuck(): void {
  setStuck(true);
}

/** clearSignInStuck: a token turned out to work after all (a socket opened). */
export function clearSignInStuck(): void {
  setStuck(false);
}

export function signInStuck(): boolean {
  return stuck;
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

export function useSignInStuck(): boolean {
  return useSyncExternalStore(subscribe, signInStuck);
}

/** bounceToRefresh sends the page for a token when the guard allows it and returns true; when it
 *  does not, it raises the banner and returns false. */
export function bounceToRefresh(): boolean {
  if (refreshAlreadyAttempted()) {
    markSignInStuck();
    return false;
  }
  markRefreshAttempted();
  redirectToRefresh(refreshReturnUrl());
  return true;
}

/** retrySignIn is the banner's Retry: the person asked, so the guard starts over. */
export function retrySignIn(): void {
  resetRefreshGuard();
  bounceToRefresh();
}
