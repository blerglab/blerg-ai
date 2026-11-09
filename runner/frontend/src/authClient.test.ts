import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { getAccessToken, consumeAccessTokenFromFragment, coreOrigin, ensureFreshToken, renewSilently, startTokenRenewal, SILENT_REFRESH_PAGE, REFRESH_LOCK } from "./authClient";

describe("authClient", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/");
  });

  it("starts with no access token", () => {
    expect(getAccessToken()).toBeNull();
  });

  it('notifies desktop consumers only when the signed-in subject changes', () => {
    const token = (sub: string, revision: number) => `h.${btoa(JSON.stringify({ sub, revision }))}.s`;
    const receive = (raw: string) => { window.history.replaceState(null, '', `/#access_token=${raw}`); consumeAccessTokenFromFragment(); };
    receive(token('person-a', 1));
    const changed = vi.fn(); window.addEventListener('blerg-identity-changed', changed);
    receive(token('person-a', 2)); expect(changed).not.toHaveBeenCalled();
    receive(token('person-b', 1)); expect(changed).toHaveBeenCalledTimes(1);
    window.removeEventListener('blerg-identity-changed', changed);
  });

  it('tells login-scoped caches when the login session changes, even for the same subject', () => {
    const token = (sub: string, sid: string, revision: number) => `h.${btoa(JSON.stringify({ kind: 'user', sub, sid, revision }))}.s`;
    const receive = (raw: string) => { window.history.replaceState(null, '', `/#access_token=${raw}`); consumeAccessTokenFromFragment(); };
    receive(token('person-a', 'one', 1));
    const login = vi.fn(), subject = vi.fn();
    window.addEventListener('blerg-login-changed', login); window.addEventListener('blerg-identity-changed', subject);
    receive(token('person-a', 'one', 2)); expect(login).not.toHaveBeenCalled();
    receive(token('person-a', 'two', 1)); expect(login).toHaveBeenCalledTimes(1); expect(subject).not.toHaveBeenCalled();
    receive(token('person-b', 'two', 1)); expect(login).toHaveBeenCalledTimes(2); expect(subject).toHaveBeenCalledTimes(1);
    window.removeEventListener('blerg-login-changed', login); window.removeEventListener('blerg-identity-changed', subject);
  });

  it("consumes an access token from the URL fragment and strips it", () => {
    window.history.replaceState(null, "", "/#access_token=abc123");
    consumeAccessTokenFromFragment();
    expect(getAccessToken()).toBe("abc123");
    expect(location.hash).toBe("");
  });

  it("leaves the access token untouched when there is no fragment to consume", () => {
    const before = getAccessToken();
    window.history.replaceState(null, "", "/somewhere");
    consumeAccessTokenFromFragment();
    expect(getAccessToken()).toBe(before);
  });

  it("preserves the path and query string when stripping the fragment", () => {
    window.history.replaceState(null, "", "/login?return_to=%2Fboards#access_token=xyz789");
    consumeAccessTokenFromFragment();
    expect(getAccessToken()).toBe("xyz789");
    expect(location.pathname).toBe("/login");
    expect(location.search).toBe("?return_to=%2Fboards");
    expect(location.hash).toBe("");
  });

  it("decodes URL-encoded characters in the token", () => {
    window.history.replaceState(null, "", "/#access_token=a%2Bb%2Fc");
    consumeAccessTokenFromFragment();
    expect(getAccessToken()).toBe("a+b/c");
  });
});

// coreOrigin: the deployment-topology derivation. Both shapes this repo ships are covered
// explicitly — k8s (install/k8s/ingress.yaml: core at the apex ${DOMAIN}, board/runner as
// subdomains of it) and desktop compose (install/desktop/docker-compose.yml: everything on
// localhost, core on BLERG_PORT_CORE, default 8081).
describe("coreOrigin", () => {
  const realLocation = window.location;

  const at = (href: string) => {
    const u = new URL(href);
    Object.defineProperty(window, "location", {
      configurable: true,
      value: {
        protocol: u.protocol,
        hostname: u.hostname,
        host: u.host,
        origin: u.origin,
        href: u.href,
        pathname: u.pathname,
        search: u.search,
        hash: u.hash,
      },
    });
  };

  afterEach(() => {
    Object.defineProperty(window, "location", { configurable: true, value: realLocation });
  });

  it("k8s: board's subdomain resolves to the apex domain core is served from", () => {
    at("https://board.blerg.example/cards");
    expect(coreOrigin()).toBe("https://board.blerg.example".replace("board.", ""));
  });

  it("k8s: runner's subdomain resolves to the apex domain too", () => {
    at("https://runner.blerg.example/sessions/1");
    expect(coreOrigin()).toBe("https://blerg.example");
  });

  it("k8s: core's own frontend, already at the apex, stays same-origin", () => {
    at("https://blerg.example/settings");
    expect(coreOrigin()).toBe("https://blerg.example");
  });

  it("desktop: a bare hostname keeps the host and swaps in core's published port", () => {
    at("http://localhost:8082/cards");
    expect(coreOrigin()).toBe("http://localhost:8081");
  });

  it("desktop: a raw IP is treated the same way as a bare hostname", () => {
    at("http://192.0.2.20:8083/");
    expect(coreOrigin()).toBe("http://192.0.2.20:8081");
  });

  it("never drops the port on the desktop shape (the old bug pointed at :80)", () => {
    at("http://localhost:8083/");
    expect(coreOrigin()).not.toBe("http://localhost");
  });

  it("never invents a core.<domain> host (the old bug's convention)", () => {
    at("https://board.blerg.example/");
    expect(coreOrigin()).not.toContain("core.");
  });

  // Dockerfiles ship `ENV VITE_CORE_PORT=""` when no build ARG is passed — an empty
  // string, not undefined. `??` only falls back on null/undefined, so it silently kept
  // the empty string and produced "http://localhost:" (port 80). `||` treats "" as
  // falsy too, so the "8081" default fires regardless of which shape Vite hands us.
  it("falls back to the default port when VITE_CORE_PORT is set but empty", () => {
    vi.stubEnv("VITE_CORE_PORT", "");
    at("http://localhost:8082/cards");
    expect(coreOrigin()).toBe("http://localhost:8081");
    vi.unstubAllEnvs();
  });
});

// ── silent renewal ───────────────────────────────────────────────────────────────────────────────
// The hidden-frame renewal is driven here with a fake frame: appendChild is intercepted so nothing
// loads, and a test plays core's part by giving the frame a location and firing its load event.

const fakeJwt = (expMs: number) => `h.${btoa(JSON.stringify({ exp: Math.floor(expMs / 1000) }))}.s`;

// A fake Web Locks API, faithful to the part of the spec this module relies on: requests queue
// behind a held lock, and a queued request that is aborted (via an AbortSignal passed in its
// options) is dropped from the queue and rejected WITHOUT ever running its callback — it never
// gets to be granted after the fact, no matter when the holder releases.
function makeFakeLocks() {
  type Entry = { cb: () => Promise<unknown>; resolve: (v: unknown) => void; reject: (e: unknown) => void; signal?: AbortSignal };
  let held = false;
  const queue: Entry[] = [];

  const run = (entry: Entry) => {
    held = true;
    void Promise.resolve()
      .then(entry.cb)
      .then(entry.resolve, entry.reject)
      .finally(() => {
        held = false;
        drain();
      });
  };

  const drain = () => {
    while (queue.length) {
      const entry = queue.shift()!;
      if (entry.signal?.aborted) continue;
      run(entry);
      return;
    }
  };

  const request = vi.fn((name: string, optsOrCb: unknown, maybeCb?: () => Promise<unknown>) => {
    expect(name).toBe(REFRESH_LOCK);
    const cb = (typeof optsOrCb === "function" ? optsOrCb : maybeCb) as () => Promise<unknown>;
    const signal = typeof optsOrCb === "function" ? undefined : (optsOrCb as { signal?: AbortSignal } | undefined)?.signal;
    return new Promise((resolve, reject) => {
      if (signal?.aborted) {
        reject(new DOMException("aborted", "AbortError"));
        return;
      }
      const entry: Entry = { cb, resolve, reject, signal };
      if (!held) {
        run(entry);
        return;
      }
      queue.push(entry);
      signal?.addEventListener("abort", () => {
        const idx = queue.indexOf(entry);
        if (idx !== -1) {
          queue.splice(idx, 1);
          entry.reject(new DOMException("aborted", "AbortError"));
        }
      });
    });
  });

  return {
    api: { request },
    isHeld: () => held,
    /** "Another tab" takes the lock directly, bypassing the module under test. */
    grabDirectly(): () => void {
      let release!: () => void;
      void request(REFRESH_LOCK, () => new Promise<void>((r) => { release = r; }));
      held = true;
      return () => release();
    },
  };
}

describe("silent renewal", () => {
  let frames: HTMLIFrameElement[];
  let removed: HTMLIFrameElement[];

  beforeEach(() => {
    vi.useFakeTimers();
    window.history.replaceState(null, "", "/");
    frames = [];
    removed = [];
    vi.spyOn(document.body, "appendChild").mockImplementation(((node: Node) => {
      const f = node as HTMLIFrameElement;
      f.remove = () => { removed.push(f); };
      frames.push(f);
      return node;
    }) as typeof document.body.appendChild);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  // core's side: the frame ended up on our own origin with the token in its fragment
  const answerWithToken = (f: HTMLIFrameElement, token: string) => {
    Object.defineProperty(f, "contentWindow", {
      configurable: true,
      value: { location: { origin: location.origin, hash: `#access_token=${encodeURIComponent(token)}` } },
    });
    f.dispatchEvent(new Event("load"));
  };
  // core's side when it cannot help: the frame is still on core's origin, which a page may not read
  const answerCrossOrigin = (f: HTMLIFrameElement) => {
    Object.defineProperty(f, "contentWindow", {
      configurable: true,
      get() { throw new DOMException("Blocked a frame from accessing a cross-origin frame.", "SecurityError"); },
    });
    f.dispatchEvent(new Event("load"));
  };

  it("asks core for a token in a hidden frame that comes back to this origin", async () => {
    const p = ensureFreshToken();
    expect(frames).toHaveLength(1);
    const f = frames[0];
    const src = new URL(f.src);
    expect(src.pathname).toBe("/auth/refresh");
    expect(src.searchParams.get("return_to")).toBe(location.origin + SILENT_REFRESH_PAGE);
    expect(f.getAttribute("aria-hidden")).toBe("true");
    expect(f.style.visibility).toBe("hidden");
    answerWithToken(f, "fresh+token/1");
    await expect(p).resolves.toBe(true);
    expect(getAccessToken()).toBe("fresh+token/1");
    expect(removed).toEqual([f]);
  });

  it("reports failure, and keeps the old token, when core answers with its own page", async () => {
    window.history.replaceState(null, "", "/#access_token=old-token");
    consumeAccessTokenFromFragment();
    const p = ensureFreshToken();
    answerCrossOrigin(frames[0]);
    await expect(p).resolves.toBe(false);
    expect(getAccessToken()).toBe("old-token");
    expect(removed).toHaveLength(1);
  });

  it("reports failure when the frame comes back without a token", async () => {
    const p = ensureFreshToken();
    Object.defineProperty(frames[0], "contentWindow", {
      configurable: true,
      value: { location: { origin: location.origin, hash: "" } },
    });
    frames[0].dispatchEvent(new Event("load"));
    await expect(p).resolves.toBe(false);
  });

  // What the frame can tell apart (renewSilently): a token; core saying the sign-in has ended (it
  // sends the frame back to this origin's silent page with #signin_error=..., see core's
  // writeSignInRequired); and everything else — core's own page, an error page for an unreachable
  // host, no load at all — which a page cannot read and so cannot tell from an outage.
  it("renews with no token held at all (a fresh load), and says so", async () => {
    const p = renewSilently();
    expect(frames).toHaveLength(1);
    expect(new URL(frames[0].src).pathname).toBe("/auth/refresh");
    answerWithToken(frames[0], "from-nothing");
    await expect(p).resolves.toBe("token");
    expect(getAccessToken()).toBe("from-nothing");
  });

  it("says the sign-in has ended when core sends the frame back with signin_error", async () => {
    const ended = vi.fn(); window.addEventListener("blerg-signed-out", ended);
    for (const reason of ["signed_out", "expired"]) {
      const p = renewSilently();
      const f = frames[frames.length - 1];
      Object.defineProperty(f, "contentWindow", {
        configurable: true,
        value: { location: { origin: location.origin, hash: `#signin_error=${reason}` } },
      });
      f.dispatchEvent(new Event("load"));
      await expect(p).resolves.toBe("signed-out");
    }
    // Login-scoped caches (kept shell scrollback) hear that it ended.
    expect(ended).toHaveBeenCalledTimes(2);
    window.removeEventListener("blerg-signed-out", ended);
    // and for the callers that only want a yes or no, it is a no
    const q = ensureFreshToken();
    const f = frames[frames.length - 1];
    Object.defineProperty(f, "contentWindow", {
      configurable: true,
      value: { location: { origin: location.origin, hash: "#signin_error=expired" } },
    });
    f.dispatchEvent(new Event("load"));
    await expect(q).resolves.toBe(false);
  });

  it("cannot tell core's own page, or no answer at all, from an outage", async () => {
    const a = renewSilently();
    answerCrossOrigin(frames[0]);
    await expect(a).resolves.toBe("no-answer");
    const b = renewSilently();
    await vi.advanceTimersByTimeAsync(15_001);
    await expect(b).resolves.toBe("no-answer");
    answerCrossOrigin(frames[1]);
    await vi.advanceTimersByTimeAsync(0);
  });

  it("gives up waiting on a slow frame, but leaves the frame to finish so a late answer still lands", async () => {
    const p = ensureFreshToken();
    await vi.advanceTimersByTimeAsync(15_001);
    await expect(p).resolves.toBe(false);
    // The request is still in flight: tearing the frame down here is how a rotation core already
    // committed gets lost, and the browser is then left holding a stale cookie.
    expect(removed).toHaveLength(0);
    answerWithToken(frames[0], "late-token");
    await vi.advanceTimersByTimeAsync(0);
    expect(getAccessToken()).toBe("late-token");
    expect(removed).toEqual([frames[0]]);
  });

  it("removes a frame that never loads at all, eventually", async () => {
    const p = ensureFreshToken();
    await vi.advanceTimersByTimeAsync(15_001);
    await expect(p).resolves.toBe(false);
    await vi.advanceTimersByTimeAsync(120_000);
    expect(removed).toHaveLength(1);
  });

  it("takes the origin's refresh lock for the frame's whole life, so other tabs wait their turn", async () => {
    const locks = makeFakeLocks();
    vi.stubGlobal("navigator", { ...navigator, locks: locks.api });
    try {
      // "Another tab" holds the lock while this tab asks.
      const releaseOther = locks.grabDirectly();
      const p = ensureFreshToken();
      await vi.advanceTimersByTimeAsync(0);
      expect(frames).toHaveLength(0); // waiting: no frame until the other tab is done
      releaseOther();
      await vi.advanceTimersByTimeAsync(0);
      expect(frames).toHaveLength(1);
      expect(locks.isHeld()).toBe(true); // and the lock is ours until the frame is gone
      answerWithToken(frames[0], "after-the-other-tab");
      await expect(p).resolves.toBe(true);
      await vi.advanceTimersByTimeAsync(0);
      expect(locks.isHeld()).toBe(false);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  // BLG-487: the caller's SILENT_TIMEOUT_MS bound must cover the wait for the lock, not just the
  // frame. Before the fix, the inner timer started only once the lock was granted, so a tab queued
  // behind another tab's lingering (up to SILENT_LINGER_MS = 120 s) frame wouldn't hear "no-answer"
  // until lock-wait + 15 s — up to 135 s, not ~15 s.
  it("falls back at SILENT_TIMEOUT_MS even while still queued behind another tab's lingering lock", async () => {
    const locks = makeFakeLocks();
    vi.stubGlobal("navigator", { ...navigator, locks: locks.api });
    try {
      locks.grabDirectly(); // another tab's frame is still lingering; never released in this test
      const p = ensureFreshToken();
      await vi.advanceTimersByTimeAsync(15_001);
      await expect(p).resolves.toBe(false);
      // The bound covered the lock wait: no frame was ever started for this caller.
      expect(frames).toHaveLength(0);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  // A lock request that gave up waiting must not go on to start a frame nobody is waiting for,
  // even if the lock becomes available afterward.
  it("does not start a frame when the lock is freed after the caller already gave up", async () => {
    const locks = makeFakeLocks();
    vi.stubGlobal("navigator", { ...navigator, locks: locks.api });
    try {
      const releaseOther = locks.grabDirectly();
      const p = ensureFreshToken();
      await vi.advanceTimersByTimeAsync(15_001);
      await expect(p).resolves.toBe(false);
      expect(frames).toHaveLength(0);
      // The other tab finally lets go, long after this caller stopped waiting.
      releaseOther();
      await vi.advanceTimersByTimeAsync(0);
      expect(frames).toHaveLength(0); // still nobody waiting: no frame should appear
      expect(locks.isHeld()).toBe(false);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  // A lock granted an instant before the bound fires must still get its frame's normal lifecycle
  // (the frame may still answer after the caller's own timeout, same as the no-lock-contention case).
  it("still lets a just-in-time grant's frame answer late, after the caller has already fallen back", async () => {
    const locks = makeFakeLocks();
    vi.stubGlobal("navigator", { ...navigator, locks: locks.api });
    try {
      const releaseOther = locks.grabDirectly();
      const p = ensureFreshToken();
      await vi.advanceTimersByTimeAsync(14_999);
      releaseOther(); // granted just before the bound
      await vi.advanceTimersByTimeAsync(0);
      expect(frames).toHaveLength(1);
      await vi.advanceTimersByTimeAsync(2);
      await expect(p).resolves.toBe(false); // the caller already gave up
      expect(removed).toHaveLength(0); // but the frame itself is never torn down early
      answerWithToken(frames[0], "late-but-landed");
      await vi.advanceTimersByTimeAsync(0);
      expect(getAccessToken()).toBe("late-but-landed");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("shares one frame between callers that ask at the same time", async () => {
    const a = ensureFreshToken();
    const b = ensureFreshToken();
    expect(frames).toHaveLength(1);
    answerWithToken(frames[0], "shared");
    await expect(Promise.all([a, b])).resolves.toEqual([true, true]);
    // and a later call starts a new one
    const c = ensureFreshToken();
    expect(frames).toHaveLength(2);
    answerCrossOrigin(frames[1]);
    await c;
  });

  it("renews shortly before the token expires, and schedules the next renewal from the new one", async () => {
    const now = Date.now();
    window.history.replaceState(null, "", `/#access_token=${fakeJwt(now + 10 * 60_000)}`);
    consumeAccessTokenFromFragment();
    startTokenRenewal();
    await vi.advanceTimersByTimeAsync(8 * 60_000);
    expect(frames).toHaveLength(0); // not yet: 90 s before expiry is 8.5 min in
    await vi.advanceTimersByTimeAsync(31_000);
    expect(frames).toHaveLength(1);
    answerWithToken(frames[0], fakeJwt(Date.now() + 10 * 60_000));
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(8 * 60_000 + 31_000);
    expect(frames).toHaveLength(2); // the next renewal, from the new token
    answerCrossOrigin(frames[1]);
    await vi.advanceTimersByTimeAsync(0);
  });

  it("tries again soon when a scheduled renewal fails and the token is still good", async () => {
    const now = Date.now();
    window.history.replaceState(null, "", `/#access_token=${fakeJwt(now + 5 * 60_000)}`);
    consumeAccessTokenFromFragment();
    startTokenRenewal();
    await vi.advanceTimersByTimeAsync(3.6 * 60_000);
    expect(frames).toHaveLength(1);
    answerCrossOrigin(frames[0]);
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(30_001);
    expect(frames).toHaveLength(2); // the retry
    answerCrossOrigin(frames[1]);
    await vi.advanceTimersByTimeAsync(0);
  });

  it("renews when a hidden tab is shown with a token that is about to expire", async () => {
    const now = Date.now();
    window.history.replaceState(null, "", `/#access_token=${fakeJwt(now + 60_000)}`);
    consumeAccessTokenFromFragment();
    startTokenRenewal();
    // the timer fires at the 5 s floor; let that attempt fail, then show the tab again
    await vi.advanceTimersByTimeAsync(5_001);
    answerCrossOrigin(frames[0]);
    await vi.advanceTimersByTimeAsync(0);
    const before = frames.length;
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(0);
    expect(frames.length).toBe(before + 1);
    answerCrossOrigin(frames[frames.length - 1]);
    await vi.advanceTimersByTimeAsync(0);
  });

  // Coverage gaps flagged in review of BLG-487's fix (no product change — the fix already
  // handles all four; these just prove it).
  it("shares one outcome between concurrent callers queued on the same lock, and a retry after giving up starts fresh", async () => {
    const locks = makeFakeLocks();
    vi.stubGlobal("navigator", { ...navigator, locks: locks.api });
    try {
      const releaseOther = locks.grabDirectly();
      const a = renewSilently();
      const b = renewSilently(); // a second caller while still queued behind the lock
      await vi.advanceTimersByTimeAsync(15_001);
      await expect(a).resolves.toBe("no-answer");
      await expect(b).resolves.toBe("no-answer"); // shared the same outcome as `a`
      expect(frames).toHaveLength(0); // the bound covered the wait: no frame was ever started
      releaseOther();
      await vi.advanceTimersByTimeAsync(0);
      expect(frames).toHaveLength(0); // still nobody waiting: no frame appears after the fact
      // A retry is a fresh call: this time nothing holds the lock, so it works normally.
      const c = renewSilently();
      await vi.advanceTimersByTimeAsync(0);
      expect(frames).toHaveLength(1);
      answerWithToken(frames[0], "after-retry");
      await expect(c).resolves.toBe("token");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("never reports signed-out, and dispatches no signed-out event, when only the lock wait times out", async () => {
    const locks = makeFakeLocks();
    vi.stubGlobal("navigator", { ...navigator, locks: locks.api });
    const signedOutEvents: Event[] = [];
    const onSignedOut = (e: Event) => signedOutEvents.push(e);
    window.addEventListener("blerg-signed-out", onSignedOut);
    try {
      locks.grabDirectly(); // another tab's frame is still lingering; never released
      const p = renewSilently();
      await vi.advanceTimersByTimeAsync(15_001);
      // Exactly "no-answer", not "signed-out": a lock-wait timeout is not core saying the
      // sign-in has ended, and must never be read as one.
      await expect(p).resolves.toBe("no-answer");
      expect(frames).toHaveLength(0);
      expect(signedOutEvents).toHaveLength(0);
    } finally {
      window.removeEventListener("blerg-signed-out", onSignedOut);
      vi.unstubAllGlobals();
    }
  });

  it("keeps the lock held for the frame's whole life even after the caller's own timeout, so a queued second tab still waits for it", async () => {
    const locks = makeFakeLocks();
    vi.stubGlobal("navigator", { ...navigator, locks: locks.api });
    try {
      const p = ensureFreshToken(); // grabs the lock immediately: nothing else holds it yet
      await vi.advanceTimersByTimeAsync(0);
      expect(frames).toHaveLength(1);

      // "Another tab" queues up behind the lock this module's own frame is holding.
      let otherTabRan = false;
      const otherTabDone = locks.api.request(REFRESH_LOCK, async () => {
        otherTabRan = true;
      });
      await vi.advanceTimersByTimeAsync(0);
      expect(otherTabRan).toBe(false); // queued: we still hold it

      await vi.advanceTimersByTimeAsync(15_001);
      await expect(p).resolves.toBe(false); // our own caller gave up at its bound
      expect(otherTabRan).toBe(false); // but the lock is still ours: the frame hasn't settled
      expect(removed).toHaveLength(0); // the frame itself is never torn down early

      answerWithToken(frames[0], "late-after-giveup"); // the frame finally answers; done settles
      await vi.advanceTimersByTimeAsync(0);
      expect(getAccessToken()).toBe("late-after-giveup");
      expect(otherTabRan).toBe(true); // only now does the queued tab get its turn
      await otherTabDone;
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("leaves no pending timers once the frame answers", async () => {
    const p = ensureFreshToken();
    answerWithToken(frames[0], "clean");
    await expect(p).resolves.toBe(true);
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("leaves no pending timers once the lock-wait bound fires", async () => {
    const locks = makeFakeLocks();
    vi.stubGlobal("navigator", { ...navigator, locks: locks.api });
    try {
      locks.grabDirectly();
      const p = ensureFreshToken();
      await vi.advanceTimersByTimeAsync(15_001);
      await expect(p).resolves.toBe(false);
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
