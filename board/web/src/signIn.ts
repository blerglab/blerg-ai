// signIn.ts — the one place this page decides to bounce to core's /auth/refresh for a token, and
// the app-level "couldn't sign you in" state when it may not (refreshGuard.ts says a bounce was
// already spent). Without that state a tab with no token showed an empty page and said nothing.
// Kept textually identical in runner/frontend/src/signIn.ts and board/web/src/signIn.ts.
//
// While that state is up the page keeps trying by itself, with the SILENT renewal only (a hidden
// frame, authClient.ts renewSilently) — never a full-page redirect, so it cannot loop and does not
// touch the redirect guard. It tries when the network comes back, when the tab is shown, and on a
// backoff (AUTO_RETRY_DELAYS_MS) that stops while the tab is hidden; one attempt at a time. A token
// clears the state and tells onSignInRecovered listeners (the runner reconnects its socket, the
// board reloads its data). Core saying the sign-in itself is gone ends the retrying: the state
// becomes "ended" and only the person's click (a full redirect, to the login form) can help.
// Anything else — core down, unreachable, slow — is retried.
import { useSyncExternalStore } from "react";
import { redirectToRefresh, renewSilently, startTokenRenewal } from "./authClient";
import {
  clearRefreshAttempted,
  markRefreshAttempted,
  refreshAlreadyAttempted,
  refreshReturnUrl,
  resetRefreshGuard,
} from "./refreshGuard";

/** How long after load a page with no token (and, in the runner, no socket) is taken to be stuck. */
export const SIGN_IN_GRACE_MS = 10_000;

/** The automatic retry's waits: after the banner shows, then after each failed attempt; the last
 *  one repeats. */
export const AUTO_RETRY_DELAYS_MS = [5_000, 15_000, 30_000, 60_000, 120_000];

/** ok: nothing to show. stuck: couldn't get a token, retrying. ended: core says the sign-in is gone. */
export type SignInState = "ok" | "stuck" | "ended";

let state: SignInState = "ok";
let epoch = 0;
const listeners = new Set<() => void>();
const recoveredListeners = new Set<() => void>();

let retryTimer: ReturnType<typeof setTimeout> | null = null;
let retryStep = 0;
let attempting = false;
let watching = false;

function setState(s: SignInState): void {
  if (state === s) return;
  state = s;
  if (s === "stuck") startAutoRetry();
  else stopAutoRetry();
  listeners.forEach((l) => l());
}

function tabHidden(): boolean {
  return typeof document !== "undefined" && document.visibilityState === "hidden";
}

function clearRetryTimer(): void {
  if (retryTimer !== null) clearTimeout(retryTimer);
  retryTimer = null;
}

function scheduleRetry(): void {
  clearRetryTimer();
  if (state !== "stuck" || attempting || tabHidden()) return;
  const delay = AUTO_RETRY_DELAYS_MS[Math.min(retryStep, AUTO_RETRY_DELAYS_MS.length - 1)];
  retryTimer = setTimeout(() => void attemptNow(), delay);
}

async function attemptNow(): Promise<void> {
  if (state !== "stuck" || attempting || tabHidden()) return;
  clearRetryTimer();
  attempting = true;
  let outcome: Awaited<ReturnType<typeof renewSilently>>;
  try {
    outcome = await renewSilently();
  } catch {
    outcome = "no-answer";
  } finally {
    attempting = false;
  }
  if (state !== "stuck") return;
  if (outcome === "token") {
    // As on any fresh token (main.tsx): drop the per-load flag so a later 401 may bounce again.
    // The rolling counter is left alone, exactly as there.
    clearRefreshAttempted();
    startTokenRenewal();
    epoch++;
    setState("ok");
    recoveredListeners.forEach((l) => {
      try {
        l();
      } catch {
        /* a listener's failure is its own */
      }
    });
    return;
  }
  if (outcome === "signed-out") {
    setState("ended");
    return;
  }
  retryStep++;
  scheduleRetry();
}

function onOnline(): void {
  void attemptNow();
}

function onVisibility(): void {
  if (tabHidden()) clearRetryTimer();
  else void attemptNow();
}

function startAutoRetry(): void {
  retryStep = 0;
  if (!watching && typeof window !== "undefined") {
    window.addEventListener("online", onOnline);
    document.addEventListener("visibilitychange", onVisibility);
    watching = true;
  }
  scheduleRetry();
}

function stopAutoRetry(): void {
  clearRetryTimer();
  if (watching) {
    window.removeEventListener("online", onOnline);
    document.removeEventListener("visibilitychange", onVisibility);
    watching = false;
  }
}

/** markSignInStuck: no token can be obtained without the person's say-so; show the banner (and
 *  start retrying quietly). A sign-in core already said has ended stays ended. */
export function markSignInStuck(): void {
  if (state === "ended") return;
  setState("stuck");
}

/** clearSignInStuck: a token turned out to work after all (a socket opened, a request succeeded). */
export function clearSignInStuck(): void {
  setState("ok");
}

export function signInStuck(): boolean {
  return state !== "ok";
}

export function signInState(): SignInState {
  return state;
}

/** signInEpoch counts tokens the automatic retry obtained; the board keys its data on it. */
export function signInEpoch(): number {
  return epoch;
}

/** onSignInRecovered: called each time the automatic retry obtained a token. */
export function onSignInRecovered(l: () => void): () => void {
  recoveredListeners.add(l);
  return () => {
    recoveredListeners.delete(l);
  };
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

export function useSignInState(): SignInState {
  return useSyncExternalStore(subscribe, signInState);
}

export function useSignInEpoch(): number {
  return useSyncExternalStore(subscribe, signInEpoch);
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

/** retrySignIn is the banner's Retry (or Sign in): the person asked, so the guard starts over. */
export function retrySignIn(): void {
  resetRefreshGuard();
  bounceToRefresh();
}
