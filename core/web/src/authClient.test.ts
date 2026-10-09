import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { getAccessToken, consumeAccessTokenFromFragment, coreOrigin, ensureFreshToken, startTokenRenewal, SILENT_REFRESH_PAGE, REFRESH_LOCK } from "./authClient";

describe("authClient", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/");
  });

  it("starts with no access token", () => {
    expect(getAccessToken()).toBeNull();
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
    // A fake Web Locks API: requests queue, and each callback holds the lock until it settles.
    const waiting: Array<() => void> = [];
    let held = false;
    const request = vi.fn(async (name: string, cb: () => Promise<unknown>) => {
      expect(name).toBe(REFRESH_LOCK);
      if (held) await new Promise<void>((r) => waiting.push(r));
      held = true;
      try {
        return await cb();
      } finally {
        held = false;
        waiting.shift()?.();
      }
    });
    vi.stubGlobal("navigator", { ...navigator, locks: { request } });
    try {
      // "Another tab" holds the lock while this tab asks.
      let releaseOther!: () => void;
      void request(REFRESH_LOCK, () => new Promise<void>((r) => { releaseOther = r; }));
      await vi.advanceTimersByTimeAsync(0);
      const p = ensureFreshToken();
      await vi.advanceTimersByTimeAsync(0);
      expect(frames).toHaveLength(0); // waiting: no frame until the other tab is done
      releaseOther();
      await vi.advanceTimersByTimeAsync(0);
      expect(frames).toHaveLength(1);
      expect(held).toBe(true); // and the lock is ours until the frame is gone
      answerWithToken(frames[0], "after-the-other-tab");
      await expect(p).resolves.toBe(true);
      await vi.advanceTimersByTimeAsync(0);
      expect(held).toBe(false);
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
});
