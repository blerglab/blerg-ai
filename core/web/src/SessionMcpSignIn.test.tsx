import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import McpConnectionsSection from "./McpConnectionsSection";
import * as authClient from "./authClient";

const id = "00000000-0000-4000-8000-000000000147";
const preview = { draft_id: id, runner_session_id: id, name: "session-server", source: "settings", resource_host: "resource.example.com", issuer_host: "issuer.example.com", provider_host: "provider.example.com", scopes: ["read"] };
const response = (status: number, body: unknown) => Promise.resolve({ ok: status >= 200 && status < 300, status, json: async () => body, text: async () => "Request refused" });

describe("session MCP sign-in in settings", () => {
  beforeEach(() => { vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-human"); });
  afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); window.history.replaceState(null, "", "/"); });
  function setup(options: { resultStatus?: number; source?: string; authorizationURL?: string } = {}) {
    const fetch = vi.fn((url: string, _init?: RequestInit) => {
      if (url === "/api/mcp/connections") return response(200, { connections: [], max: 20 });
      if (url.endsWith("/preview")) return response(200, { ...preview, source: options.source ?? "settings" });
      if (url.endsWith("/start")) return response(200, { authorization_url: options.authorizationURL ?? "https://provider.example.com/authorize?state=synthetic", authorization_server: "provider.example.com" });
      if (url === `/api/mcp/session-oauth/${id}`) return response(options.resultStatus ?? 200, { stage: "completed", preview });
      if (url.endsWith("/cancel")) return response(204, {});
      return response(404, {});
    });
    vi.stubGlobal("fetch", fetch);
    render(<McpConnectionsSection />);
    return fetch;
  }
  it("requires human review and shows the provider host before navigation", async () => {
    window.history.replaceState(null, "", `/?mcp_runner_session=${id}&mcp_definition=${id}&scopes=write&url=https://attacker.example.com`);
    const fetch = setup();
    const review = await screen.findByRole("button", { name: "Review session sign-in" });
    expect(screen.getByLabelText("Scopes for this session (optional)")).toHaveValue("");
    expect(fetch.mock.calls.some(([url]) => url.endsWith("/preview"))).toBe(false);
    fireEvent.change(screen.getByLabelText("Scopes for this session (optional)"), { target: { value: "read" } });
    fireEvent.click(review);
    expect(await screen.findByText("provider.example.com")).toBeInTheDocument();
    expect(screen.getByText("issuer.example.com")).toBeInTheDocument();
    expect(screen.getByText("From settings")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue to provider.example.com" })).toBeInTheDocument();
    expect(fetch.mock.calls.some(([url]) => url.endsWith("/start"))).toBe(false);
    const call = fetch.mock.calls.find(([url]) => url.endsWith("/preview"));
    expect(JSON.parse(call![1]!.body as string)).toEqual({ runner_session_id: id, definition_id: id, scopes: "read" });
    expect(screen.queryByDisplayValue("https://attacker.example.com")).not.toBeInTheDocument();
  });
  it("labels a suggestion only after its human-saved definition is resolved", async () => {
    window.history.replaceState(null, "", `/?mcp_runner_session=${id}&mcp_definition=${id}`);
    setup({ source: "session_suggestion_saved_by_human" });
    fireEvent.click(await screen.findByRole("button", { name: "Review session sign-in" }));
    expect(await screen.findByText("Suggested by this session")).toBeInTheDocument();
  });
  it("fetches authoritative status instead of trusting a success query", async () => {
    window.history.replaceState(null, "", `/?mcp_session_result=${id}&mcp_session_oauth=success`);
    setup({ resultStatus: 404 });
    expect(screen.queryByText(/Signed in for this session/)).not.toBeInTheDocument();
    expect(await screen.findByText(/This sign-in result is no longer available/)).toBeInTheDocument();
    expect(screen.queryByText(/Signed in for this session/)).not.toBeInTheDocument();
  });
  it("shows Signed in only after Core confirms completion, never Connected", async () => {
    window.history.replaceState(null, "", `/?mcp_session_result=${id}`);
    const fetch = setup();
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(`/api/mcp/session-oauth/${id}`, expect.anything()));
    expect(await screen.findByText(/Signed in for this session/)).toBeInTheDocument();
    expect(screen.queryByText(/Connected/)).not.toBeInTheDocument();
  });
  it.each(["http://provider.example.com/authorize", "https://attacker.example.com/authorize", "https://synthetic@provider.example.com/authorize", "https://provider.example.com/authorize#fragment"])("refuses an invalid start destination %s", async authorizationURL => {
    window.history.replaceState(null, "", `/?mcp_runner_session=${id}&mcp_definition=${id}`);
    setup({ authorizationURL });
    fireEvent.click(await screen.findByRole("button", { name: "Review session sign-in" }));
    fireEvent.click(await screen.findByRole("button", { name: "Continue to provider.example.com" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Sign-in could not be completed");
  });
  it("cancels the server attempt before dismissing the preview", async () => {
    window.history.replaceState(null, "", `/?mcp_runner_session=${id}&mcp_definition=${id}`);
    const fetch = setup();
    fireEvent.click(await screen.findByRole("button", { name: "Review session sign-in" }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel sign-in" }));
    expect(await screen.findByText("Sign-in was canceled.")).toBeInTheDocument();
    expect(fetch).toHaveBeenCalledWith(`/api/mcp/session-oauth/${id}/cancel`, expect.objectContaining({ method: "POST" }));
    expect(screen.queryByRole("button", { name: "Continue to provider.example.com" })).not.toBeInTheDocument();
  });

});
