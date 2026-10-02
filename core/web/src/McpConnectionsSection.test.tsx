import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import McpConnectionsSection from "./McpConnectionsSection";
import * as authClient from "./authClient";

type Conn = {
  id: string;
  name: string;
  url: string;
  auth_kind: string;
  header_name?: string;
  has_secret: boolean;
  status: string;
  oauth_issuer?: string;
  last_verified_at: string | null;
};

const conn = (over: Partial<Conn> = {}): Conn => ({
  id: "11111111-1111-1111-1111-111111111111",
  name: "docs",
  url: "https://mcp.example.com/mcp",
  auth_kind: "static",
  header_name: "Authorization",
  has_secret: true,
  status: "ok",
  last_verified_at: null,
  ...over,
});

const reply = (status: number, body: unknown) =>
  Promise.resolve({
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(typeof body === "string" ? body : JSON.stringify(body)),
  });

// A tiny fake of core's /api/mcp/connections routes. `reject` makes the next write answer with
// a status and plain-text reason.
function fakeCore(initial: Conn[] = [], max = 20) {
  const state = {
    conns: initial,
    calls: [] as { url: string; method: string; body: Record<string, string> | null }[],
    reject: null as { status: number; text: string } | null,
    authURL: "https://auth.example.com/authorize?state=abc",
  };
  const fetchMock = vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const body = init?.body ? (JSON.parse(init.body as string) as Record<string, string>) : null;
    state.calls.push({ url, method, body });
    if (url === "/api/mcp/connections" && method === "GET") return reply(200, { connections: state.conns, max });
    if (method !== "GET" && state.reject) return reply(state.reject.status, state.reject.text);
    if (url === "/api/mcp/connections/oauth/start" && method === "POST") {
      return reply(200, { authorization_url: state.authURL });
    }
    if (url.endsWith("/reconnect") && method === "POST") return reply(200, { authorization_url: state.authURL });
    if (url === "/api/mcp/connections" && method === "POST") {
      const c = conn({
        id: `id-${state.conns.length + 1}`,
        name: body!.name,
        url: body!.url,
        auth_kind: body!.auth_kind,
        header_name: body!.header_name,
        has_secret: body!.auth_kind === "static",
      });
      state.conns = [...state.conns, c];
      return reply(201, c);
    }
    const id = url.split("/").pop()!;
    if (method === "PATCH") {
      state.conns = state.conns.map((c) => (c.id === id ? { ...c, name: body!.name ?? c.name } : c));
      return reply(200, state.conns.find((c) => c.id === id));
    }
    if (method === "DELETE") {
      state.conns = state.conns.filter((c) => c.id !== id);
      return Promise.resolve({ ok: true, status: 204 });
    }
    return reply(404, "not found");
  });
  return { state, fetchMock };
}

describe("MCP connections", () => {
  let core: ReturnType<typeof fakeCore>;
  const setup = (initial: Conn[] = [], max = 20) => {
    core = fakeCore(initial, max);
    vi.stubGlobal("fetch", core.fetchMock);
  };
  const fillAdd = (name: string, url: string, secret = "") => {
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: name } });
    fireEvent.change(screen.getByLabelText(/Server URL/), { target: { value: url } });
    if (secret) fireEvent.change(screen.getByLabelText(/^Secret/), { target: { value: secret } });
  };

  beforeEach(() => {
    sessionStorage.clear();
    vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-access-token");
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    sessionStorage.clear();
  });

  it("explains the feature in the empty state and points at the runner app", async () => {
    setup();
    render(<McpConnectionsSection site={{ version: "x", board_url: "", runner_url: "https://runner.test" }} />);
    await screen.findByText(/No MCP connections yet/);
    const help = document.querySelector(".section-help")!.textContent ?? "";
    expect(help).toMatch(/off for every session until you select them/);
    expect(help).toMatch(/Choosing the tools is done in the runner app/);
    expect(screen.getByRole("link", { name: "runner app" })).toHaveAttribute("href", "https://runner.test");
    expect(screen.getByText("0 of 20 connections used.")).toBeInTheDocument();
  });

  it("renders the runner app as plain text when the runner url is unknown", async () => {
    setup();
    render(<McpConnectionsSection />);
    await screen.findByText(/No MCP connections yet/);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("lists connections with name, url, auth kind, status and last verified", async () => {
    setup([
      conn(),
      conn({ id: "2", name: "open", url: "https://open.example.com/mcp", auth_kind: "none", has_secret: false, status: "needs_auth", last_verified_at: "2026-01-02T03:04:05Z" }),
      conn({ id: "3", name: "broken", status: "error", header_name: "X-Api-Key" }),
    ]);
    render(<McpConnectionsSection />);
    await screen.findByText("docs");
    expect(screen.getAllByText("https://mcp.example.com/mcp")).toHaveLength(2);
    expect(screen.getByText("https://open.example.com/mcp")).toBeInTheDocument();
    expect(screen.getByText(/Header Authorization \(secret stored\)\. Never verified\./)).toBeInTheDocument();
    expect(screen.getByText(/No authentication\. Verified /)).toBeInTheDocument();
    expect(screen.getByText("OK")).toBeInTheDocument();
    expect(screen.getByText("Needs auth")).toBeInTheDocument();
    expect(screen.getByText("Error")).toBeInTheDocument();
    expect(screen.getByText("3 of 20 connections used.")).toBeInTheDocument();
    // A connection without a secret has no replace-secret action.
    expect(screen.queryByRole("button", { name: "Replace secret for open" })).not.toBeInTheDocument();
  });

  it("gives every input an id and a label, and defaults the header to Authorization", async () => {
    setup();
    render(<McpConnectionsSection />);
    await screen.findByText(/No MCP connections yet/);
    for (const el of document.querySelectorAll("input, select")) {
      expect(el.id).not.toBe("");
      expect(document.querySelector(`label[for="${el.id}"]`)).not.toBeNull();
    }
    expect(screen.getByLabelText("Header name")).toHaveValue("Authorization");
    expect(screen.getByLabelText(/^Secret/)).toHaveAttribute("type", "password");
  });

  it("disables Add until name, url and (for static) secret are filled", async () => {
    setup();
    render(<McpConnectionsSection />);
    await screen.findByText(/No MCP connections yet/);
    const add = screen.getByRole("button", { name: "Add connection" });
    expect(add).toBeDisabled();
    fillAdd("docs", "https://mcp.example.com/mcp");
    expect(add).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/^Secret/), { target: { value: "tok" } });
    expect(add).toBeEnabled();
    // A connection with no authentication needs no secret and hides the fields.
    fireEvent.change(screen.getByLabelText("Authentication"), { target: { value: "none" } });
    expect(screen.queryByLabelText(/^Secret/)).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Header name")).not.toBeInTheDocument();
  });

  it("adds a static connection, clears the form and never renders the secret", async () => {
    setup();
    render(<McpConnectionsSection />);
    await screen.findByText(/No MCP connections yet/);
    fillAdd(" docs ", " https://mcp.example.com/mcp ", "s3cret-value");
    fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
    await screen.findByText("1 of 20 connections used.");
    const post = core.state.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({
      name: "docs",
      url: "https://mcp.example.com/mcp",
      auth_kind: "static",
      header_name: "Authorization",
      secret: "s3cret-value",
    });
    expect(screen.getByLabelText(/^Secret/)).toHaveValue("");
    expect(screen.getByLabelText(/^Name/)).toHaveValue("");
    expect(document.body.innerHTML).not.toContain("s3cret-value");
    expect((screen.getByLabelText(/^Secret/) as HTMLInputElement).value).toBe("");
    expect(screen.getByText(/secret stored/)).toBeInTheDocument();
  });

  it("adds a connection with no authentication and sends no secret", async () => {
    setup();
    render(<McpConnectionsSection />);
    await screen.findByText(/No MCP connections yet/);
    fireEvent.change(screen.getByLabelText("Authentication"), { target: { value: "none" } });
    fillAdd("open", "https://open.example.com/mcp");
    fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
    await screen.findByText("open");
    expect(core.state.calls.find((c) => c.method === "POST")!.body).toEqual({
      name: "open",
      url: "https://open.example.com/mcp",
      auth_kind: "none",
    });
  });

  it("shows the server's reason for a rejected add, keeps the fields, and clears the secret", async () => {
    setup();
    render(<McpConnectionsSection />);
    await screen.findByText(/No MCP connections yet/);
    core.state.reject = { status: 400, text: "url must use https" };
    fillAdd("docs", "http://mcp.example.com", "s3cret-value");
    fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("url must use https");
    expect(screen.getByLabelText(/Server URL/)).toHaveValue("http://mcp.example.com");
    expect(screen.getByLabelText(/^Secret/)).toHaveValue("");
    expect(screen.queryByText("1 of 20 connections used.")).not.toBeInTheDocument();
  });

  it("shows a 409 name clash and does not render a 500 body", async () => {
    setup();
    render(<McpConnectionsSection />);
    await screen.findByText(/No MCP connections yet/);
    core.state.reject = { status: 409, text: "another connection already uses this name" };
    fillAdd("docs", "https://mcp.example.com/mcp", "x");
    fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("another connection already uses this name");

    core.state.reject = { status: 500, text: "<b>stack trace</b>" };
    fireEvent.change(screen.getByLabelText(/^Secret/), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Failed to add the connection (500)."));
    expect(screen.getByRole("alert")).not.toHaveTextContent("stack trace");
  });

  it("surfaces a failed load", async () => {
    core = fakeCore();
    core.fetchMock.mockImplementationOnce(() => reply(500, "boom"));
    vi.stubGlobal("fetch", core.fetchMock);
    render(<McpConnectionsSection />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Failed to load MCP connections (500).");
    expect(screen.queryByRole("button", { name: "Add connection" })).not.toBeInTheDocument();
  });

  it("shows the limit and hides the add form when it is reached", async () => {
    setup([conn(), conn({ id: "2", name: "other" })], 2);
    render(<McpConnectionsSection />);
    await screen.findByText("2 of 2 connections used.");
    expect(screen.getByText(/reached the limit of 2 connections/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add connection" })).not.toBeInTheDocument();
  });

  it("renames a connection with a PATCH", async () => {
    setup([conn()]);
    render(<McpConnectionsSection />);
    await screen.findByText("docs");
    fireEvent.click(screen.getByRole("button", { name: "Rename docs" }));
    const input = screen.getByLabelText("New name");
    expect(input).toHaveValue("docs");
    fireEvent.change(input, { target: { value: "wiki" } });
    fireEvent.click(screen.getByRole("button", { name: "Save name" }));
    await screen.findByText("wiki");
    const patch = core.state.calls.find((c) => c.method === "PATCH")!;
    expect(patch.url).toBe("/api/mcp/connections/11111111-1111-1111-1111-111111111111");
    expect(patch.body).toEqual({ name: "wiki" });
    expect(screen.queryByLabelText("New name")).not.toBeInTheDocument();
  });

  it("replaces a secret, sends it once and never shows it", async () => {
    setup([conn()]);
    render(<McpConnectionsSection />);
    await screen.findByText("docs");
    fireEvent.click(screen.getByRole("button", { name: "Replace secret for docs" }));
    const input = screen.getByLabelText("New secret");
    expect(input).toHaveAttribute("type", "password");
    fireEvent.change(input, { target: { value: "rotated-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Save secret" }));
    await waitFor(() => expect(screen.queryByLabelText("New secret")).not.toBeInTheDocument());
    expect(core.state.calls.find((c) => c.method === "PATCH")!.body).toEqual({ secret: "rotated-secret" });
    expect(document.body.innerHTML).not.toContain("rotated-secret");
  });

  it("asks for confirmation in the page before removing, and cancel keeps it", async () => {
    const confirm = vi.spyOn(window, "confirm");
    setup([conn()]);
    render(<McpConnectionsSection />);
    await screen.findByText("docs");
    fireEvent.click(screen.getByRole("button", { name: "Remove docs" }));
    expect(screen.getByText(/Remove docs\? Sessions and crons that use it lose access/)).toBeInTheDocument();
    expect(core.state.calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByRole("button", { name: "Remove docs" })).toBeInTheDocument();
    expect(core.state.calls.some((c) => c.method === "DELETE")).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "Remove docs" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm remove" }));
    await screen.findByText(/No MCP connections yet/);
    expect(core.state.calls.find((c) => c.method === "DELETE")!.url).toBe(
      "/api/mcp/connections/11111111-1111-1111-1111-111111111111",
    );
    expect(confirm).not.toHaveBeenCalled();
  });

  it("surfaces a failed remove and keeps the connection", async () => {
    setup([conn()]);
    render(<McpConnectionsSection />);
    await screen.findByText("docs");
    core.state.reject = { status: 500, text: "oops" };
    fireEvent.click(screen.getByRole("button", { name: "Remove docs" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm remove" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Failed to remove the connection (500).");
    expect(screen.getByText("docs")).toBeInTheDocument();
  });

  describe("OAuth", () => {
    const realLocation = window.location;
    let assign: ReturnType<typeof vi.fn>;
    beforeEach(() => {
      assign = vi.fn();
      Object.defineProperty(window, "location", {
        configurable: true,
        value: { assign, search: "", pathname: "/settings", hash: "", href: "http://localhost/settings" },
      });
    });
    afterEach(() => {
      Object.defineProperty(window, "location", { configurable: true, value: realLocation });
    });
    const oauthConn = (over: Partial<Conn> = {}) =>
      conn({
        id: "oa-1",
        name: "cal",
        auth_kind: "oauth",
        header_name: undefined,
        has_secret: false,
        oauth_issuer: "https://auth.example.com/tenant",
        ...over,
      });
    const chooseOAuth = () => fireEvent.change(screen.getByLabelText("Authentication"), { target: { value: "oauth" } });

    it("offers three authentication kinds and switches the fields", async () => {
      setup();
      render(<McpConnectionsSection />);
      await screen.findByText(/No MCP connections yet/);
      const kind = screen.getByLabelText("Authentication") as HTMLSelectElement;
      expect(Array.from(kind.options).map((o) => [o.value, o.text])).toEqual([
        ["static", "Token (static)"],
        ["oauth", "Sign in (OAuth)"],
        ["none", "No credential"],
      ]);
      chooseOAuth();
      expect(screen.getByLabelText(/^Name/)).toBeInTheDocument();
      expect(screen.getByLabelText(/Server URL/)).toBeInTheDocument();
      expect(
        screen.getByLabelText("Client ID (only if the server does not register clients automatically)"),
      ).toBeInTheDocument();
      expect(screen.getByLabelText(/^Scopes/)).toBeInTheDocument();
      expect(screen.queryByLabelText(/^Secret/)).not.toBeInTheDocument();
      expect(screen.queryByLabelText("Header name")).not.toBeInTheDocument();
      expect(screen.getByText(/sent to the server's sign-in page and brought back/)).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
      fillAdd("cal", "https://mcp.example.com/mcp");
      expect(screen.getByRole("button", { name: "Connect" })).toBeEnabled();
      fireEvent.change(kind, { target: { value: "none" } });
      expect(screen.queryByLabelText(/^Scopes/)).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Add connection" })).toBeEnabled();
    });

    it("starts the flow with only the filled fields and navigates to the authorization url", async () => {
      setup();
      render(<McpConnectionsSection />);
      await screen.findByText(/No MCP connections yet/);
      chooseOAuth();
      fillAdd(" cal ", " https://mcp.example.com/mcp ");
      fireEvent.click(screen.getByRole("button", { name: "Connect" }));
      await waitFor(() => expect(assign).toHaveBeenCalledWith("https://auth.example.com/authorize?state=abc"));
      const post = core.state.calls.find((c) => c.url === "/api/mcp/connections/oauth/start")!;
      expect(post.method).toBe("POST");
      expect(post.body).toEqual({ name: "cal", url: "https://mcp.example.com/mcp" });
    });

    it("sends client id and scopes when given", async () => {
      setup();
      render(<McpConnectionsSection />);
      await screen.findByText(/No MCP connections yet/);
      chooseOAuth();
      fillAdd("cal", "https://mcp.example.com/mcp");
      fireEvent.change(screen.getByLabelText(/^Client ID/), { target: { value: " cid " } });
      fireEvent.change(screen.getByLabelText(/^Scopes/), { target: { value: " read write " } });
      fireEvent.click(screen.getByRole("button", { name: "Connect" }));
      await waitFor(() => expect(assign).toHaveBeenCalled());
      expect(core.state.calls.find((c) => c.url === "/api/mcp/connections/oauth/start")!.body).toEqual({
        name: "cal",
        url: "https://mcp.example.com/mcp",
        client_id: "cid",
        scopes: "read write",
      });
    });

    it.each(["http://auth.example.com/a", "javascript:alert(1)", "not a url", ""])(
      "refuses the authorization url %j",
      async (bad) => {
        setup();
        render(<McpConnectionsSection />);
        await screen.findByText(/No MCP connections yet/);
        core.state.authURL = bad;
        chooseOAuth();
        fillAdd("cal", "https://mcp.example.com/mcp");
        fireEvent.click(screen.getByRole("button", { name: "Connect" }));
        expect(await screen.findByRole("alert")).toHaveTextContent(/not a valid https sign-in address/);
        expect(assign).not.toHaveBeenCalled();
      },
    );

    it("shows the server's reason when start is rejected", async () => {
      setup();
      render(<McpConnectionsSection />);
      await screen.findByText(/No MCP connections yet/);
      core.state.reject = { status: 400, text: "server does not support sign-in" };
      chooseOAuth();
      fillAdd("cal", "https://mcp.example.com/mcp");
      fireEvent.click(screen.getByRole("button", { name: "Connect" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("server does not support sign-in");
      expect(assign).not.toHaveBeenCalled();
    });

    it("renders an OAuth row with kind, issuer host and only Rename, Reconnect and Remove", async () => {
      setup([oauthConn()]);
      render(<McpConnectionsSection />);
      await screen.findByText("cal");
      const meta = screen.getByTestId("mcp-auth-oa-1");
      expect(meta).toHaveTextContent("OAuth");
      expect(meta).toHaveTextContent("auth.example.com");
      expect(meta.textContent).not.toContain("/tenant");
      expect(screen.getByRole("button", { name: "Rename cal" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Reconnect cal" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Remove cal" })).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /Replace secret/ })).not.toBeInTheDocument();
    });

    it("explains needs_auth and reconnects", async () => {
      setup([oauthConn({ status: "needs_auth" })]);
      render(<McpConnectionsSection />);
      await screen.findByText("cal");
      expect(
        screen.getByText(
          "Sign-in expired or was revoked — sessions and crons that use this connection cannot run until you reconnect",
        ),
      ).toBeInTheDocument();
      const btn = screen.getByRole("button", { name: "Reconnect cal" });
      expect(btn).toHaveClass("btn-primary");
      fireEvent.click(btn);
      await waitFor(() => expect(assign).toHaveBeenCalledWith("https://auth.example.com/authorize?state=abc"));
      const post = core.state.calls.find((c) => c.url === "/api/mcp/connections/oa-1/reconnect")!;
      expect(post.method).toBe("POST");
      expect(post.body).toEqual({});
    });

    it("refuses a non-https reconnect url", async () => {
      setup([oauthConn({ status: "needs_auth" })]);
      render(<McpConnectionsSection />);
      await screen.findByText("cal");
      core.state.authURL = "http://evil.example.com/";
      fireEvent.click(screen.getByRole("button", { name: "Reconnect cal" }));
      expect(await screen.findByRole("alert")).toHaveTextContent(/not a valid https sign-in address/);
      expect(assign).not.toHaveBeenCalled();
    });

    it("shows error status clearly", async () => {
      setup([oauthConn({ status: "error" })]);
      render(<McpConnectionsSection />);
      await screen.findByText("cal");
      expect(screen.getByText("Error")).toBeInTheDocument();
      expect(screen.getByText(/could not be reached or verified/)).toBeInTheDocument();
    });

    it("says OAuth removal also revokes access at the provider", async () => {
      setup([oauthConn()]);
      render(<McpConnectionsSection />);
      await screen.findByText("cal");
      fireEvent.click(screen.getByRole("button", { name: "Remove cal" }));
      expect(screen.getByText(/revoked at the provider where the provider supports it/)).toBeInTheDocument();
    });
  });

  describe("OAuth return banner", () => {
    const at = (qs: string) => window.history.pushState(null, "", `/settings${qs}`);
    afterEach(() => window.history.replaceState(null, "", "/"));

    it("shows a success banner, cleans the url, and highlights the connection", async () => {
      const scroll = vi.fn();
      Element.prototype.scrollIntoView = scroll;
      setup([conn({ id: "abc", name: "cal", auth_kind: "oauth", has_secret: false })]);
      at("?mcp_oauth=success&connection=abc&keep=1#x");
      const spy = vi.spyOn(window.history, "replaceState");
      render(<McpConnectionsSection />);
      expect(await screen.findByTestId("mcp-oauth-banner")).toHaveTextContent(/Connected/);
      expect(spy).toHaveBeenCalled();
      expect(window.location.search).toBe("?keep=1");
      expect(window.location.hash).toBe("#x");
      await waitFor(() => expect(scroll).toHaveBeenCalled());
      expect(screen.getByText("cal").closest("li")).toHaveAttribute("data-highlight", "true");
      fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
      expect(screen.queryByTestId("mcp-oauth-banner")).not.toBeInTheDocument();
    });

    it.each([
      ["state", /expired or was already used/],
      ["denied", /denied/],
      ["exchange", /could not finish the sign-in/],
      ["session", /signed in to Blerg/],
      ["redirect_uri", /redirect address/],
      ["issuer", /unexpected sign-in server/],
      ["name_taken", /name is already in use/],
      ["limit", /limit/],
      ["gone", /no longer exists/],
      ["internal", /went wrong on our side/],
    ])("maps reason %s", async (reason, re) => {
      setup();
      at(`?mcp_oauth=error&reason=${reason}`);
      render(<McpConnectionsSection />);
      const banner = await screen.findByTestId("mcp-oauth-banner");
      expect(banner).toHaveTextContent(re);
      expect(banner).toHaveAttribute("role", "alert");
      expect(window.location.search).toBe("");
    });

    it("gives a generic message for an unknown reason and never shows raw query text", async () => {
      setup();
      at("?mcp_oauth=error&reason=%3Cb%3Eevil%3C%2Fb%3E&connection=zzz");
      render(<McpConnectionsSection />);
      const banner = await screen.findByTestId("mcp-oauth-banner");
      expect(banner).toHaveTextContent(/Could not connect/);
      expect(document.body.textContent).not.toContain("evil");
      expect(window.location.search).toBe("");
    });

    it("shows no banner for an unknown mcp_oauth value", async () => {
      setup();
      at("?mcp_oauth=bogus&x=1");
      render(<McpConnectionsSection />);
      await screen.findByText(/No MCP connections yet/);
      expect(screen.queryByTestId("mcp-oauth-banner")).not.toBeInTheDocument();
    });
  });
});
