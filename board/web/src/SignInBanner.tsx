import { useEffect } from "react";
import { getAccessToken } from "./authClient";
import { SIGN_IN_GRACE_MS, markSignInStuck, retrySignIn, useSignInStuck } from "./signIn";

/**
 * SignInBanner — one line at the top of the board when no token can be obtained: the refresh
 * bounce was already spent (signIn.ts), or ten seconds after load there is still no token. Without
 * it the board showed an empty page and said nothing. Retry starts the refresh guard over and goes
 * for a token again. The runner's twin is runner/frontend/src/components/SignInBanner.tsx.
 */
export default function SignInBanner() {
  const stuck = useSignInStuck();
  useEffect(() => {
    const t = setTimeout(() => {
      if (!getAccessToken()) markSignInStuck();
    }, SIGN_IN_GRACE_MS);
    return () => clearTimeout(t);
  }, []);
  if (!stuck) return null;
  return (
    <div role="alert" className="signin-banner" data-testid="sign-in-banner">
      <span>Couldn't sign you in</span>
      <span aria-hidden="true">·</span>
      <button type="button" onClick={retrySignIn}>Retry</button>
    </div>
  );
}
