import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { getAccessToken, consumeAccessTokenFromFragment, coreOrigin } from "./authClient";

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
