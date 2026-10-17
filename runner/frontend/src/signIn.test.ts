// signIn.test.ts — the banner's automatic retry. Kept textually identical in
// runner/frontend/src/signIn.test.ts and board/web/src/signIn.test.ts, like signIn.ts itself.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const { redirectToRefresh, renewSilently, startTokenRenewal } = vi.hoisted(() => ({
  redirectToRefresh: vi.fn(),
  renewSilently: vi.fn<() => Promise<"token" | "signed-out" | "no-answer">>(),
  startTokenRenewal: vi.fn(),
}));

vi.mock("./authClient", () => ({ redirectToRefresh, renewSilently, startTokenRenewal }));

import {
  AUTO_RETRY_DELAYS_MS,
  clearSignInStuck,
  markSignInStuck,
  onSignInRecovered,
  retrySignIn,
  signInEpoch,
  signInState,
} from "./signIn";
import { MAX_ATTEMPTS, markRefreshAttempted, refreshAlreadyAttempted } from "./refreshGuard";

let hidden = false;
const setHidden = (h: boolean) => {
  hidden = h;
  document.dispatchEvent(new Event("visibilitychange"));
};

describe("signIn: automatic retry while the banner is up", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    redirectToRefresh.mockClear();
    renewSilently.mockReset();
    renewSilently.mockResolvedValue("no-answer");
    startTokenRenewal.mockClear();
    sessionStorage.clear();
    hidden = false;
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (hidden ? "hidden" : "visible") });
    Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
    clearSignInStuck();
  });

  afterEach(() => {
    clearSignInStuck();
    vi.useRealTimers();
  });

  it("backs off 5 s, 15 s, 30 s, 60 s, then every 120 s", async () => {
    expect(AUTO_RETRY_DELAYS_MS).toEqual([5_000, 15_000, 30_000, 60_000, 120_000]);
    markSignInStuck();
    // [time since the banner, attempts by then]
    const checkpoints: Array<[number, number]> = [
      [4_999, 0], [5_000, 1], [19_999, 1], [20_000, 2], [50_000, 3], [109_999, 3], [110_000, 4],
      [229_999, 4], [230_000, 5], [350_000, 6],
    ];
    let t = 0;
    for (const [at, n] of checkpoints) {
      await vi.advanceTimersByTimeAsync(at - t);
      t = at;
      expect([at, renewSilently.mock.calls.length]).toEqual([at, n]);
    }
    expect(signInState()).toBe("stuck");
    expect(redirectToRefresh).not.toHaveBeenCalled(); // never a full-page redirect on its own
  });

  it("a token from the timer path clears the banner, tells listeners, and keeps renewing", async () => {
    const recovered = vi.fn();
    const off = onSignInRecovered(recovered);
    const epoch = signInEpoch();
    markSignInAttemptedAndStuck();
    renewSilently.mockResolvedValueOnce("no-answer").mockResolvedValueOnce("token");
    await vi.advanceTimersByTimeAsync(5_000);
    expect(signInState()).toBe("stuck");
    expect(recovered).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(15_000);
    expect(signInState()).toBe("ok");
    expect(recovered).toHaveBeenCalledTimes(1);
    expect(signInEpoch()).toBe(epoch + 1);
    expect(startTokenRenewal).toHaveBeenCalledTimes(1);
    // the per-load flag is dropped as on any fresh token; the rolling counter is left alone
    expect(sessionStorage.getItem("blerg_refresh_attempted")).toBeNull();
    expect(JSON.parse(sessionStorage.getItem("blerg_refresh_attempts")!).n).toBe(1);
    // and nothing runs once it is cleared
    await vi.advanceTimersByTimeAsync(600_000);
    expect(renewSilently).toHaveBeenCalledTimes(2);
    off();
  });

  it("the network coming back retries at once", async () => {
    markSignInStuck();
    renewSilently.mockResolvedValueOnce("token");
    window.dispatchEvent(new Event("online"));
    await vi.advanceTimersByTimeAsync(0);
    expect(renewSilently).toHaveBeenCalledTimes(1);
    expect(signInState()).toBe("ok");
  });

  it("a hidden tab does not retry until it is shown", async () => {
    markSignInStuck();
    setHidden(true);
    await vi.advanceTimersByTimeAsync(600_000);
    window.dispatchEvent(new Event("online"));
    await vi.advanceTimersByTimeAsync(0);
    expect(renewSilently).not.toHaveBeenCalled();
    renewSilently.mockResolvedValueOnce("token");
    setHidden(false);
    await vi.advanceTimersByTimeAsync(0);
    expect(renewSilently).toHaveBeenCalledTimes(1);
    expect(signInState()).toBe("ok");
  });

  it("going hidden mid-backoff stops the timer", async () => {
    markSignInStuck();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(renewSilently).toHaveBeenCalledTimes(1);
    setHidden(true);
    await vi.advanceTimersByTimeAsync(600_000);
    expect(renewSilently).toHaveBeenCalledTimes(1);
  });

  it("the sign-in having ended stops retrying, and the click still does the full redirect", async () => {
    markSignInStuck();
    renewSilently.mockResolvedValueOnce("signed-out");
    await vi.advanceTimersByTimeAsync(5_000);
    expect(signInState()).toBe("ended");
    await vi.advanceTimersByTimeAsync(600_000);
    window.dispatchEvent(new Event("online"));
    setHidden(true);
    setHidden(false);
    await vi.advanceTimersByTimeAsync(0);
    expect(renewSilently).toHaveBeenCalledTimes(1);
    // a later failure elsewhere (a refused socket) does not turn it back into retrying
    markSignInStuck();
    await vi.advanceTimersByTimeAsync(600_000);
    expect(signInState()).toBe("ended");
    expect(renewSilently).toHaveBeenCalledTimes(1);
    retrySignIn();
    expect(redirectToRefresh).toHaveBeenCalledTimes(1);
  });

  it("one attempt in flight at a time: no storm from events and timers", async () => {
    let answer!: (o: "token" | "signed-out" | "no-answer") => void;
    renewSilently.mockImplementation(() => new Promise((r) => { answer = r; }));
    markSignInStuck();
    window.dispatchEvent(new Event("online"));
    window.dispatchEvent(new Event("online"));
    setHidden(false);
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(600_000);
    expect(renewSilently).toHaveBeenCalledTimes(1);
    answer("no-answer");
    await vi.advanceTimersByTimeAsync(0);
    // the next one waits its turn on the backoff, not on the events above
    expect(renewSilently).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(15_000);
    expect(renewSilently).toHaveBeenCalledTimes(2);
  });

  it("the full-redirect guard stays as strict: automatic retries never spend or clear the counter", async () => {
    for (let i = 0; i < MAX_ATTEMPTS; i++) markRefreshAttempted();
    markSignInStuck();
    await vi.advanceTimersByTimeAsync(600_000);
    expect(redirectToRefresh).not.toHaveBeenCalled();
    expect(refreshAlreadyAttempted()).toBe(true);
    expect(JSON.parse(sessionStorage.getItem("blerg_refresh_attempts")!).n).toBe(MAX_ATTEMPTS);
  });
});

function markSignInAttemptedAndStuck(): void {
  markRefreshAttempted();
  markSignInStuck();
}
