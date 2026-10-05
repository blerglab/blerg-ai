import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import Settings from "./Settings";
import * as authClient from "./authClient";

function buildFetchMock(store: { engines: string[] }, captureBody: { value?: Record<string, unknown> }) {
  return vi.fn((url: string, init?: RequestInit) => {
    if (url === "/api/credentials" && (!init || init.method === undefined)) {
      return Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve(
            store.engines.map((engine) => ({ engine, updated_at: "2026-09-16T00:00:00.000Z" })),
          ),
      });
    }
    if (url === "/api/credentials" && init?.method === "POST") {
      const body = JSON.parse(init.body as string) as Record<string, unknown>;
      body.__headers = init.headers as unknown as Record<string, unknown>;
      captureBody.value = body;
      const engine = body.engine as string;
      if (!store.engines.includes(engine)) store.engines.push(engine);
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
    }
    if (url.startsWith("/api/credentials/") && init?.method === "DELETE") {
      const engine = url.slice("/api/credentials/".length);
      store.engines = store.engines.filter((e) => e !== engine);
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
  });
}

// ── agent tokens (spec §2) ────────────────────────────────────────────────────
// A token fixture as GET /api/tokens serves it (core/internal/api/token_handlers.go's
// tokenSummaryResponse): metadata only, never the token value.
type TokenSummary = {
  id: string;
  name: string;
  aud: string;
  caps: string[];
  created_at: string;
  expires_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
};

const SITE = {
  version: "test",
  board_url: "https://board.example.test",
  runner_url: "https://runner.example.test",
};

function tokenFixture(over: Partial<TokenSummary> = {}): TokenSummary {
  return {
    id: "tok_1",
    name: "laptop cli",
    aud: "blerg-runner",
    caps: ["session.start"],
    created_at: "2026-09-01T10:00:00Z",
    expires_at: "2099-01-01T10:00:00Z",
    last_used_at: null,
    revoked_at: null,
    ...over,
  };
}

// buildTokenFetchMock serves /api/site, an empty /api/credentials list, and the three
// token endpoints out of a mutable store, recording the POST body for assertions.
function buildTokenFetchMock(
  store: { tokens: TokenSummary[] },
  captured: { post?: Record<string, unknown>; postHeaders?: Record<string, string> },
) {
  return vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (url === "/api/site") {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(SITE) });
    }
    if (url === "/api/credentials") {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([]) });
    }
    if (url === "/api/tokens" && method === "GET") {
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve(store.tokens),
      });
    }
    if (url === "/api/tokens" && method === "POST") {
      captured.post = JSON.parse(init!.body as string) as Record<string, unknown>;
      captured.postHeaders = init!.headers as Record<string, string>;
      const created = tokenFixture({
        id: "tok_new",
        name: captured.post.name as string,
        aud:
          captured.post.preset === "board"
            ? "blerg-board"
            : captured.post.preset === "platform"
              ? "blerg-core"
              : "blerg-runner",
      });
      store.tokens = [...store.tokens, created];
      return Promise.resolve({
        ok: true,
        status: 201,
        json: () => Promise.resolve({ ...created, token: "blergat_secret_value" }),
      });
    }
    if (url.startsWith("/api/tokens/") && method === "DELETE") {
      const id = url.slice("/api/tokens/".length);
      store.tokens = store.tokens.map((t) =>
        t.id === id ? { ...t, revoked_at: "2026-09-20T00:00:00Z" } : t,
      );
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) });
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) });
  });
}

describe("Settings — agent tokens", () => {
  let store: { tokens: TokenSummary[] };
  let captured: { post?: Record<string, unknown>; postHeaders?: Record<string, string> };

  beforeEach(() => {
    store = { tokens: [] };
    captured = {};
    // refreshGuard keeps its "already redirected" flag in sessionStorage, which jsdom shares
    // across tests in a file — a 401 test would otherwise suppress the redirect in later ones.
    sessionStorage.clear();
    vi.stubGlobal("fetch", buildTokenFetchMock(store, captured));
    vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-access-token");
    vi.spyOn(authClient, "ensureFreshToken").mockResolvedValue(false); // no quiet renewal in these tests
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    sessionStorage.clear();
  });

  const create = async (preset?: string, name = "laptop cli") => {
    fireEvent.change(screen.getByLabelText("Token name"), { target: { value: name } });
    if (preset) {
      fireEvent.change(screen.getByLabelText("Preset"), { target: { value: preset } });
    }
    fireEvent.click(screen.getByRole("button", { name: "Create token" }));
  };

  it("renders the Agent tokens section with its explanation and the three presets", async () => {
    render(<Settings />);
    await screen.findByText("Agent tokens");

    expect(screen.getByText("Automation")).toBeInTheDocument();
    expect(screen.getByText(/Tokens for tools and agents that act as you\./)).toBeInTheDocument();
    // The two rules a user cannot infer from the table: per-owner session scope, and caps
    // frozen at mint.
    expect(
      screen.getByText(/A run-sessions token can only see sessions it started; admins see all\./),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/changing your role later does not change an existing token/),
    ).toBeInTheDocument();

    const select = screen.getByLabelText("Preset") as HTMLSelectElement;
    const options = Array.from(select.options).map((o) => o.textContent);
    expect(options).toEqual([
      "run-sessions — Start and drive agent sessions on the runner (REST and MCP)",
      "board — Read and write cards on the board (REST and MCP)",
      "platform — Read your identity and credential status on core",
    ]);
    expect(select.value).toBe("run-sessions");
    expect((screen.getByLabelText("Expires in (days)") as HTMLInputElement).value).toBe("90");
  });

  it("posts name, preset and expiry with the bearer header", async () => {
    render(<Settings />);
    await screen.findByText("Agent tokens");

    fireEvent.change(screen.getByLabelText("Expires in (days)"), { target: { value: "30" } });
    await create("board", "ci pipeline");

    await waitFor(() => expect(captured.post).toBeDefined());
    expect(captured.post).toMatchObject({
      name: "ci pipeline",
      preset: "board",
      expires_in_days: 30,
    });
    expect(captured.postHeaders!.Authorization).toBe("Bearer test-access-token");
  });

  it("reveals the token once with a copy button and a ready-to-paste curl line", async () => {
    render(<Settings />);
    await screen.findByText("Agent tokens");
    await create();

    const reveal = await screen.findByLabelText("New token");
    expect((reveal as HTMLInputElement).value).toBe("blergat_secret_value");
    expect(reveal).toHaveAttribute("readonly");

    // run-sessions is the runner's audience, so the example hits the runner's base URL — and
    // an AUTHENTICATED route there, so a 200 actually proves the token works (/agents is
    // public and would answer 200 for a revoked token just as happily).
    expect(
      screen.getByText(
        'curl -H "Authorization: Bearer blergat_secret_value" https://runner.example.test/api/runner/me',
      ),
    ).toBeInTheDocument();

    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith("blergat_secret_value");
  });

  it("survives a browser with no clipboard API", async () => {
    render(<Settings />);
    await screen.findByText("Agent tokens");
    await create();
    await screen.findByLabelText("New token");

    vi.stubGlobal("navigator", { ...navigator, clipboard: undefined });
    expect(() => fireEvent.click(screen.getByRole("button", { name: "Copy" }))).not.toThrow();
    expect((screen.getByLabelText("New token") as HTMLInputElement).value).toBe(
      "blergat_secret_value",
    );
  });

  it("uses the board base URL for a board token and core's own origin for platform", async () => {
    const { unmount } = render(<Settings />);
    await screen.findByText("Agent tokens");
    await create("board");
    await screen.findByLabelText("New token");
    expect(
      screen.getByText(
        'curl -H "Authorization: Bearer blergat_secret_value" https://board.example.test/api/boards',
      ),
    ).toBeInTheDocument();
    unmount();

    render(<Settings />);
    await screen.findByText("Agent tokens");
    await create("platform", "core reader");
    await screen.findByLabelText("New token");
    expect(
      screen.getByText(
        `curl -H "Authorization: Bearer blergat_secret_value" ${location.origin}/api/me`,
      ),
    ).toBeInTheDocument();
  });

  it("keeps the curl example pointed at the minted token's component", async () => {
    // Changing the select after minting must not repoint the example at a component the
    // revealed token has no audience for.
    render(<Settings />);
    await screen.findByText("Agent tokens");
    await create("board");
    await screen.findByLabelText("New token");

    fireEvent.change(screen.getByLabelText("Preset"), { target: { value: "run-sessions" } });

    expect(
      screen.getByText(
        'curl -H "Authorization: Bearer blergat_secret_value" https://board.example.test/api/boards',
      ),
    ).toBeInTheDocument();
  });

  it("never shows the token again once Done is clicked", async () => {
    render(<Settings />);
    await screen.findByText("Agent tokens");
    await create();
    await screen.findByLabelText("New token");

    fireEvent.click(screen.getByRole("button", { name: "Done" }));

    await waitFor(() => expect(screen.queryByLabelText("New token")).not.toBeInTheDocument());
    expect(screen.queryByText(/blergat_secret_value/)).not.toBeInTheDocument();
    // The freshly minted token is still in the list — by name, never by value.
    expect(screen.getByText("laptop cli")).toBeInTheDocument();
  });

  it("lists tokens with preset, audience, last used and Active/Revoked/Expired status", async () => {
    store.tokens = [
      tokenFixture({ id: "a", name: "active one", last_used_at: "2026-09-18T08:00:00Z" }),
      tokenFixture({
        id: "b",
        name: "revoked one",
        aud: "blerg-board",
        revoked_at: "2026-09-19T08:00:00Z",
      }),
      tokenFixture({
        id: "c",
        name: "expired one",
        aud: "blerg-core",
        expires_at: "2020-01-01T00:00:00Z",
      }),
    ];
    render(<Settings />);
    await screen.findByText("active one");

    const rowOf = (name: string) => screen.getByText(name).closest("tr")!;
    expect(rowOf("active one")).toHaveTextContent("run-sessions");
    expect(rowOf("active one")).toHaveTextContent("blerg-runner");
    expect(rowOf("active one")).toHaveTextContent("Active");
    expect(rowOf("revoked one")).toHaveTextContent("board");
    expect(rowOf("revoked one")).toHaveTextContent("Revoked");
    expect(rowOf("expired one")).toHaveTextContent("platform");
    expect(rowOf("expired one")).toHaveTextContent("Expired");
    // Never-used tokens say so rather than rendering an empty cell.
    expect(rowOf("revoked one")).toHaveTextContent("Never");
  });

  it("shows an empty state when there are no tokens", async () => {
    render(<Settings />);
    await screen.findByText("Agent tokens");
    expect(await screen.findByText("No agent tokens yet.")).toBeInTheDocument();
  });

  it("revokes a token with a DELETE carrying the bearer header and refreshes", async () => {
    store.tokens = [tokenFixture({ id: "tok_9", name: "doomed" })];
    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    render(<Settings />);
    await screen.findByText("doomed");

    fireEvent.click(screen.getByRole("button", { name: "Revoke" }));

    await waitFor(() => {
      expect(screen.getByText("doomed").closest("tr")!).toHaveTextContent("Revoked");
    });
    const del = fetchMock.mock.calls.find(
      (c: unknown[]) => (c[1] as RequestInit | undefined)?.method === "DELETE",
    )!;
    expect(del[0]).toBe("/api/tokens/tok_9");
    expect(((del[1] as RequestInit).headers as Record<string, string>).Authorization).toBe(
      "Bearer test-access-token",
    );
    // A revoked token offers no second Revoke.
    expect(screen.queryByRole("button", { name: "Revoke" })).not.toBeInTheDocument();
  });

  it("says the session expired rather than claiming there are no tokens, on a 401", async () => {
    // Settings now runs two gated loads at once (credentials and tokens). On an expired
    // session the first 401 spends the refresh guard's one redirect and the second is handed
    // its 401 back — which must not render as a load failure or as an empty token list.
    vi.spyOn(authClient, "redirectToRefresh").mockImplementation(() => {});
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve({ ok: false, status: 401, json: () => Promise.resolve({}) })),
    );
    render(<Settings />);

    await waitFor(() =>
      expect(
        screen.queryAllByText("Your session has expired. Reload the page to sign in again."),
      ).not.toHaveLength(0),
    );
    expect(screen.queryByText("No agent tokens yet.")).not.toBeInTheDocument();
    expect(screen.queryByText(/Failed to load/)).not.toBeInTheDocument();
  });

  it("reports a failed create without revealing anything", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        if (url === "/api/tokens" && init?.method === "POST") {
          return Promise.resolve({ ok: false, status: 400, json: () => Promise.resolve({}) });
        }
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([]) });
      }),
    );
    render(<Settings />);
    await screen.findByText("Agent tokens");
    await create();

    expect(await screen.findByText(/Failed to create token \(400\)\./)).toBeInTheDocument();
    expect(screen.queryByLabelText("New token")).not.toBeInTheDocument();
  });

  it("shows the backend's own message when it gives one", async () => {
    const message = "too many active agent tokens (limit 50); revoke one first";
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        if (url === "/api/tokens" && init?.method === "POST") {
          return Promise.resolve({
            ok: false,
            status: 409,
            json: () => Promise.resolve({ error: message }),
          });
        }
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([]) });
      }),
    );
    render(<Settings />);
    await screen.findByText("Agent tokens");
    await create();

    // The actionable message, not a bare "(409)" the user can do nothing with.
    expect(await screen.findByText(message)).toBeInTheDocument();
    expect(screen.queryByText(/Failed to create token/)).not.toBeInTheDocument();
  });

  it("ignores a non-JSON or shapeless error body", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        if (url === "/api/tokens" && init?.method === "POST") {
          return Promise.resolve({
            ok: false,
            status: 502,
            // A proxy's HTML page: json() rejects, and nothing of it may be rendered.
            json: () => Promise.reject(new Error("not json")),
          });
        }
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([]) });
      }),
    );
    render(<Settings />);
    await screen.findByText("Agent tokens");
    await create();

    expect(await screen.findByText(/Failed to create token \(502\)\./)).toBeInTheDocument();
  });
});

describe("Settings", () => {
  let store: { engines: string[] };
  let captureBody: { value?: Record<string, unknown> };

  beforeEach(() => {
    store = { engines: [] };
    captureBody = {};
    vi.stubGlobal("fetch", buildFetchMock(store, captureBody));
    vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-access-token");
    vi.spyOn(authClient, "ensureFreshToken").mockResolvedValue(false); // no quiet renewal in these tests
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("explains these credentials are for cluster-runtime sessions, which run as you", async () => {
    render(<Settings />);
    await screen.findByText("Claude");
    expect(
      screen.getByText(/Used by cluster-runtime sessions, which run as you\./),
    ).toBeInTheDocument();
  });

  it("says per kind what desktop sessions use: git tokens yes, engine credentials no", async () => {
    render(<Settings />);
    await screen.findByText("Claude");
    const help = screen.getByText(/Used by cluster-runtime sessions/);
    expect(help.textContent).toMatch(/engine credentials aren't needed there/);
    expect(help.textContent).toMatch(/GitHub and GitLab tokens are/);
    expect(help.textContent).not.toMatch(/nothing here is needed/);
    // The sandbox has no git credentials: never promise a push from it.
    expect(screen.queryByText(/clone and push as you/)).toBeNull();
    expect(screen.getAllByText(/can commit but not push/).length).toBe(2);
  });

  it("shows not configured for all five credential kinds on empty state", async () => {
    render(<Settings />);
    await screen.findByText("Claude");
    const statuses = screen.getAllByText("Not configured");
    expect(statuses).toHaveLength(5);
    expect(screen.getByText("Claude")).toBeInTheDocument();
    expect(screen.getByText("Codex")).toBeInTheDocument();
    expect(screen.getByText("Hermes")).toBeInTheDocument();
    expect(screen.getByText("GitHub")).toBeInTheDocument();
    expect(screen.getByText("GitLab")).toBeInTheDocument();
    expect(screen.queryByText("OpenClaw")).not.toBeInTheDocument();
  });

  it("groups the engine credentials apart from the git credential", async () => {
    render(<Settings />);
    await screen.findByText("Claude");
    const engines = screen.getByText("Engines").closest("div")!;
    expect(engines).toHaveTextContent("Claude");
    expect(engines).toHaveTextContent("Codex");
    expect(engines).toHaveTextContent("Hermes");
    expect(engines).not.toHaveTextContent("GitHub");
    expect(engines).not.toHaveTextContent("GitLab");
    const git = screen.getByText("Git").closest("div")!;
    expect(git).toHaveTextContent("GitHub");
    expect(git).toHaveTextContent("GitLab");
    expect(git).not.toHaveTextContent("Codex");
  });

  it("explains how to generate each credential", async () => {
    render(<Settings />);
    await screen.findByText("Claude");

    expect(screen.getByText("Claude").closest("li")).toHaveTextContent("claude setup-token");
    expect(screen.getByText("Codex").closest("li")).toHaveTextContent("~/.codex/auth.json");
    expect(screen.getByText("Hermes").closest("li")).toHaveTextContent("~/.hermes/.env");
    expect(screen.getByText("GitHub").closest("li")).toHaveTextContent(
      "fine-grained personal access token",
    );

    const githubLink = screen.getByRole("link", {
      name: "https://github.com/settings/personal-access-tokens/new",
    });
    expect(githubLink).toHaveAttribute("href", "https://github.com/settings/personal-access-tokens/new");
    expect(githubLink).toHaveAttribute("target", "_blank");
    expect(githubLink).toHaveAttribute("rel", "noopener noreferrer");

    const gitlabItem = screen.getByText("GitLab").closest("li")!;
    expect(gitlabItem).toHaveTextContent("read_api");
    expect(gitlabItem).toHaveTextContent("write_repository");
    const gitlabLink = screen.getByRole("link", {
      name: "https://gitlab.com/-/user_settings/personal_access_tokens",
    });
    expect(gitlabLink).toHaveAttribute("href", "https://gitlab.com/-/user_settings/personal_access_tokens");
    expect(gitlabLink).toHaveAttribute("target", "_blank");
    expect(gitlabLink).toHaveAttribute("rel", "noopener noreferrer");

    // The console link reads as part of the sentence, like GitHub's — the
    // href is what matters, not a bare URL rendered as text.
    const claudeLink = screen.getByRole("link", { name: "Anthropic console" });
    expect(claudeLink).toHaveAttribute("href", "https://console.anthropic.com/settings/keys");
    expect(claudeLink).toHaveAttribute("target", "_blank");
    expect(claudeLink).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("removes a configured credential with a DELETE and flips the chip back", async () => {
    store.engines = ["github"];
    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    render(<Settings />);
    await screen.findByText("GitHub");
    const githubItem = screen.getByText("GitHub").closest("li")!;
    expect(githubItem).toHaveTextContent("Configured");

    fireEvent.click(
      Array.from(githubItem.querySelectorAll("button")).find((b) => b.textContent === "Remove")!,
    );

    await waitFor(() => {
      expect(screen.getByText("GitHub").closest("li")!).toHaveTextContent("Not configured");
    });
    const del = fetchMock.mock.calls.find(
      (c: unknown[]) => (c[1] as RequestInit | undefined)?.method === "DELETE",
    )!;
    expect(del[0]).toBe("/api/credentials/github");
    expect(((del[1] as RequestInit).headers as Record<string, string>).Authorization).toBe(
      "Bearer test-access-token",
    );
  });

  it("offers Remove only for credentials that are configured", async () => {
    store.engines = ["codex"];
    render(<Settings />);
    await screen.findByText("Codex");
    expect(screen.getAllByText("Remove")).toHaveLength(1);
    expect(screen.getByText("Codex").closest("li")).toHaveTextContent("Remove");
    expect(screen.getByText("Claude").closest("li")).not.toHaveTextContent("Remove");
  });

  it("pastes in a credential, posts it with the bearer header, and refreshes to configured", async () => {
    render(<Settings />);
    await screen.findByText("Claude");

    const textarea = screen.getByLabelText("Claude credential");
    fireEvent.change(textarea, { target: { value: "sk-super-secret" } });
    const claudeForm = textarea.closest("form")!;
    fireEvent.click(
      Array.from(claudeForm.querySelectorAll("button")).find((b) => b.textContent === "Save credential")!,
    );

    await waitFor(() => expect(captureBody.value).toBeDefined());
    expect(captureBody.value!.engine).toBe("claude");
    expect(captureBody.value!.credential).toBe("sk-super-secret");
    const headers = captureBody.value!.__headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer test-access-token");

    // List refreshes to "configured" for claude, and the pasted value is never shown again.
    await waitFor(() => {
      const claudeItem = screen.getByText("Claude").closest("li")!;
      expect(claudeItem).toHaveTextContent("Configured");
    });
    expect(screen.queryByText("sk-super-secret")).not.toBeInTheDocument();
    expect((screen.getByLabelText("Claude credential") as HTMLTextAreaElement).value).toBe("");

    // The other four kinds remain not configured.
    expect(screen.getAllByText("Not configured")).toHaveLength(4);
  });

  it("never re-displays a previously saved credential's value", async () => {
    store.engines = ["codex"];
    render(<Settings />);
    await screen.findByText("Codex");
    const codexItem = screen.getByText("Codex").closest("li")!;
    expect(codexItem).toHaveTextContent("Configured");
    // The paste-in field for an already-configured engine starts empty, not pre-filled.
    expect((screen.getByLabelText("Codex credential") as HTMLTextAreaElement).value).toBe("");
  });

  it("recovers from a 401 by redirecting to core's /auth/refresh instead of dead-ending", async () => {
    // Task 18 gave board and runner a redirectToRefresh-on-401 path; core/web had none,
    // so an expired session here rendered a bare "Failed to load credential status (401)"
    // with nothing to click.
    const redirect = vi.spyOn(authClient, "redirectToRefresh").mockImplementation(() => {});
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve({ ok: false, status: 401, json: () => Promise.resolve({}) })),
    );

    render(<Settings />);

    await waitFor(() => expect(redirect).toHaveBeenCalled());
    expect(screen.queryByText(/Failed to load credential status/)).not.toBeInTheDocument();
  });

  it("omits the Authorization header entirely when there is no access token", async () => {
    vi.spyOn(authClient, "getAccessToken").mockReturnValue(null);
    vi.spyOn(authClient, "ensureFreshToken").mockResolvedValue(false); // no quiet renewal in these tests
    const fetchMock = vi.fn((_url: string, _init?: RequestInit) =>
      Promise.resolve({ ok: true, json: () => Promise.resolve([]) }),
    );
    vi.stubGlobal("fetch", fetchMock);

    render(<Settings />);

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const init = fetchMock.mock.calls[0][1] as RequestInit | undefined;
    const headers = (init?.headers ?? {}) as Record<string, string>;
    expect(headers.Authorization).toBeUndefined();
    expect(JSON.stringify(headers)).not.toContain("Bearer null");
  });
});
