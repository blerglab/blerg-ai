import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import GitReadOnlyTokensSection, { parseGitReadOnlyEntries } from "./GitReadOnlyTokensSection";
import { apiFetch } from "./apiFetch";

vi.mock("./apiFetch", () => ({ apiFetch: vi.fn() }));
const fetchMock = vi.mocked(apiFetch);
const response = (value: unknown, ok = true, status = 200) =>
  ({ ok, status, json: async () => value, text: async () => (typeof value === "string" ? value : JSON.stringify(value)) }) as Response;
async function settle() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

beforeEach(() => fetchMock.mockReset());
afterEach(() => cleanup());

describe("parseGitReadOnlyEntries", () => {
  it("picks out git-readonly:* rows and ignores everything else", () => {
    const entries = parseGitReadOnlyEntries([
      { engine: "git-readonly:github:acme-corp", updated_at: "2026-01-01T00:00:00Z" },
      { engine: "git-readonly:gitlab:personal-account", updated_at: "2026-01-02T00:00:00Z", last_used_at: "2026-01-03T00:00:00Z" },
      { engine: "claude", updated_at: "2026-01-01T00:00:00Z" },
      { engine: "github", updated_at: "2026-01-01T00:00:00Z" },
      // malformed shapes must never reach the list, even defensively (core already refuses these).
      { engine: "git-readonly:github:", updated_at: "2026-01-01T00:00:00Z" },
      { engine: "git-readonly:bitbucket:acme", updated_at: "2026-01-01T00:00:00Z" },
    ]);
    expect(entries).toEqual([
      { engine: "git-readonly:github:acme-corp", provider: "github", owner: "acme-corp", lastUsedAt: null },
      { engine: "git-readonly:gitlab:personal-account", provider: "gitlab", owner: "personal-account", lastUsedAt: "2026-01-03T00:00:00Z" },
    ]);
  });
});

describe("GitReadOnlyTokensSection", () => {
  it("shows no tokens yet when the account has none", () => {
    render(<GitReadOnlyTokensSection summaries={[]} onChanged={vi.fn()} />);
    expect(screen.getByText("No read-only tokens yet.")).toBeTruthy();
  });

  it("lists an existing token with its owner, provider and last-used date, never its value", () => {
    render(
      <GitReadOnlyTokensSection
        summaries={[{ engine: "git-readonly:github:acme-corp", updated_at: "2026-01-01T00:00:00Z", last_used_at: "2026-02-03T12:00:00Z" }]}
        onChanged={vi.fn()}
      />,
    );
    const row = screen.getByText("acme-corp").closest("tr")!;
    expect(within(row).getByText("GitHub")).toBeTruthy();
    expect(within(row).getByText("Feb 3, 2026")).toBeTruthy();
    expect(screen.queryByText(/ghp_|github_pat_/)).toBeNull();
  });

  it("shows \"Never used\" for a token that has never been fetched", () => {
    render(<GitReadOnlyTokensSection summaries={[{ engine: "git-readonly:gitlab:acme", updated_at: "2026-01-01T00:00:00Z" }]} onChanged={vi.fn()} />);
    expect(screen.getByText("Never used")).toBeTruthy();
  });

  it("masks the pasted token and keeps it out of spellcheck/autofill (BLG-460 finding 1)", () => {
    render(<GitReadOnlyTokensSection summaries={[]} onChanged={vi.fn()} />);
    const field = screen.getByLabelText("Read-only token") as HTMLInputElement;
    expect(field.type).toBe("password");
    expect(field.autocomplete).toBe("off");
    expect(field.getAttribute("spellcheck")).toBe("false");
    expect(field.getAttribute("autocapitalize")).toBe("off");
  });

  it("tells the person Blerg refuses classic tokens and checks GitLab scopes", () => {
    render(<GitReadOnlyTokensSection summaries={[]} onChanged={vi.fn()} />);
    expect(screen.getByText(/fine-grained token with Contents: read-only/)).toBeTruthy();
    expect(screen.getByText(/Blerg refuses classic tokens and checks GitLab scopes/)).toBeTruthy();
  });

  it("adds a token: posts the composite engine key, clears the draft, and refreshes", async () => {
    const onChanged = vi.fn().mockResolvedValue(true);
    render(<GitReadOnlyTokensSection summaries={[]} onChanged={onChanged} />);
    fetchMock.mockResolvedValueOnce(response({}));
    fireEvent.change(screen.getByLabelText("Owner"), { target: { value: "acme-corp" } });
    fireEvent.change(screen.getByLabelText("Read-only token"), { target: { value: "ghp_readonly_secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Add token" }));
    await settle();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/credentials",
      expect.objectContaining({ method: "POST", body: JSON.stringify({ engine: "git-readonly:github:acme-corp", credential: "ghp_readonly_secret" }) }),
    );
    expect(onChanged).toHaveBeenCalledTimes(1);
    expect(screen.getByLabelText("Owner")).toHaveValue("");
    expect(screen.getByLabelText("Read-only token")).toHaveValue("");
    // The pasted value must never reappear anywhere in the rendered page.
    expect(screen.queryByText("ghp_readonly_secret")).toBeNull();
  });

  it("selects GitLab as the provider for the composite engine key", async () => {
    const onChanged = vi.fn().mockResolvedValue(true);
    render(<GitReadOnlyTokensSection summaries={[]} onChanged={onChanged} />);
    fetchMock.mockResolvedValueOnce(response({}));
    fireEvent.change(screen.getByLabelText("Provider"), { target: { value: "gitlab" } });
    fireEvent.change(screen.getByLabelText("Owner"), { target: { value: "acme" } });
    fireEvent.change(screen.getByLabelText("Read-only token"), { target: { value: "glpat-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Add token" }));
    await settle();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/credentials",
      expect.objectContaining({ body: JSON.stringify({ engine: "git-readonly:gitlab:acme", credential: "glpat-secret" }) }),
    );
  });

  it("shows a save refusal without claiming the token was added", async () => {
    const onChanged = vi.fn();
    render(<GitReadOnlyTokensSection summaries={[]} onChanged={onChanged} />);
    fetchMock.mockResolvedValueOnce(response("owner already in use", false, 400));
    fireEvent.change(screen.getByLabelText("Owner"), { target: { value: "acme-corp" } });
    fireEvent.change(screen.getByLabelText("Read-only token"), { target: { value: "ghp_secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Add token" }));
    await settle();
    expect(await screen.findByRole("alert")).toHaveTextContent("Failed to save the read-only token for acme-corp: owner already in use");
    expect(onChanged).not.toHaveBeenCalled();
  });

  it("removes a token by its exact, URL-encoded composite engine key and refreshes", async () => {
    const onChanged = vi.fn().mockResolvedValue(true);
    render(
      <GitReadOnlyTokensSection
        summaries={[{ engine: "git-readonly:github:acme-corp", updated_at: "2026-01-01T00:00:00Z" }]}
        onChanged={onChanged}
      />,
    );
    fetchMock.mockResolvedValueOnce(response({}));
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await settle();
    expect(fetchMock).toHaveBeenCalledWith(`/api/credentials/${encodeURIComponent("git-readonly:github:acme-corp")}`, expect.objectContaining({ method: "DELETE" }));
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it("shows a remove refusal and leaves the row in place", async () => {
    render(
      <GitReadOnlyTokensSection
        summaries={[{ engine: "git-readonly:github:acme-corp", updated_at: "2026-01-01T00:00:00Z" }]}
        onChanged={vi.fn()}
      />,
    );
    fetchMock.mockResolvedValueOnce(response("", false, 500));
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await settle();
    expect(await screen.findByRole("alert")).toHaveTextContent("Failed to remove the read-only token for acme-corp (500).");
    expect(screen.getByText("acme-corp")).toBeTruthy();
  });
});
