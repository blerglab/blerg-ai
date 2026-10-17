import { useEffect } from "react";
import { getAccessToken } from "./authClient";
import { SIGN_IN_GRACE_MS, markSignInStuck, retrySignIn, useSignInState } from "./signIn";

/**
 * SignInBanner — one line at the top of the board when no token can be obtained: the refresh
 * bounce was already spent (signIn.ts), or ten seconds after load there is still no token. Without
 * it the board showed an empty page and said nothing. While it shows, signIn.ts keeps trying quietly
 * and takes it down when a token arrives (App reloads its data then); Retry starts the refresh guard
 * over and goes for a token at once. When core says the sign-in itself has ended it reads "Your
 * sign-in has ended · Sign in" and stops trying; the click is the same full redirect, which lands on
 * the login form. The runner's twin is runner/frontend/src/components/SignInBanner.tsx.
 */
export default function SignInBanner() {
  const state = useSignInState();
  useEffect(() => {
    const t = setTimeout(() => {
      if (!getAccessToken()) markSignInStuck();
    }, SIGN_IN_GRACE_MS);
    return () => clearTimeout(t);
  }, []);
  if (state === "ok") return null;
  const ended = state === "ended";
  return (
    <div role="alert" className="signin-banner" data-testid="sign-in-banner">
      <span>{ended ? "Your sign-in has ended" : "Couldn't sign you in"}</span>
      <span aria-hidden="true">·</span>
      <button type="button" onClick={retrySignIn}>{ended ? "Sign in" : "Retry"}</button>
    </div>
  );
}
