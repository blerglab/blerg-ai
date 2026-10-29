import { beforeEach, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import PluginMarketplacesSection, { validMarketplace } from "./PluginMarketplacesSection";

const mock = vi.hoisted(() => ({ fetch: vi.fn() }));
vi.mock("./apiFetch", () => ({ apiFetch: mock.fetch }));
const defaults = ["anthropics/claude-plugins-official", "codex:openai-curated", "codex:openai-curated-remote"];
beforeEach(() => mock.fetch.mockReset());
it("keeps defaults, saves toggles, validates additions and removes custom marketplaces", async () => {
  let policy = { revision: 1, marketplaces: defaults.map(source => ({ source, enabled: true })), any_marketplace: false };
  mock.fetch.mockImplementation(async (_url: string, init?: RequestInit) => {
    if (init?.method === "PUT") policy = { ...JSON.parse(init.body as string), revision: policy.revision + 1 };
    return { ok: true, json: async () => structuredClone(policy) };
  });
  const changed = vi.fn();
  window.addEventListener("plugin-marketplaces-changed", changed);
  render(<PluginMarketplacesSection />);
  const official = await screen.findByRole("switch", { name: "Allow codex:openai-curated-remote" });
  expect(official).toHaveAttribute("aria-checked", "true");
  expect(screen.queryByRole("button", { name: /Remove codex:/ })).not.toBeInTheDocument();
  fireEvent.click(official);
  await waitFor(() => expect(official).toHaveAttribute("aria-checked", "false"));
  expect(policy.marketplaces).toHaveLength(3);
  expect(changed).toHaveBeenCalledOnce();
  const input = screen.getByLabelText(/Add marketplace \(owner/);
  for (const source of ["../escape", "https://example.com/plugins", "codex:UPPER", defaults[0]]) {
    fireEvent.change(input, { target: { value: source } });
    expect(screen.getByRole("button", { name: "Add marketplace" })).toBeDisabled();
    expect(input).toHaveAttribute("aria-invalid", "true");
  }
  fireEvent.change(input, { target: { value: "codex:custom" } });
  fireEvent.click(screen.getByRole("button", { name: "Add marketplace" }));
  const remove = await screen.findByRole("button", { name: "Remove codex:custom" });
  expect(policy.marketplaces).toContainEqual({ source: "codex:custom", enabled: true });
  fireEvent.click(remove);
  await waitFor(() => expect(screen.queryByRole("button", { name: "Remove codex:custom" })).not.toBeInTheDocument());
  expect(policy.marketplaces).toHaveLength(3);
  window.removeEventListener("plugin-marketplaces-changed", changed);
});
it("reports a stale policy conflict without claiming a saved change", async () => {
  mock.fetch.mockResolvedValueOnce({ ok: true, json: async () => ({ revision: 1, marketplaces: defaults.map(source => ({ source, enabled: true })), any_marketplace: false }) });
  mock.fetch.mockResolvedValueOnce({ ok: false, status: 409 });
  render(<PluginMarketplacesSection />);
  fireEvent.click(await screen.findByRole("switch", { name: `Allow ${defaults[0]}` }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Reload");
  expect(screen.queryByRole("status")).not.toBeInTheDocument();
  expect(screen.getByRole("switch", { name: `Allow ${defaults[0]}` })).toHaveAttribute("aria-checked", "true");
});
it("retains explicit wildcard policy and explains why individual toggles are disabled", async () => {
  mock.fetch.mockResolvedValueOnce({ ok: true, json: async () => ({ revision: 1, marketplaces: defaults.map(source => ({ source, enabled: true })), any_marketplace: true }) });
  render(<PluginMarketplacesSection />);
  expect(await screen.findByRole("switch", { name: "Allow any valid marketplace" })).toHaveAttribute("aria-checked", "true");
  expect(screen.getByRole("switch", { name: `Allow ${defaults[0]}` })).toBeDisabled();
  expect(screen.getByText(/Turn this off to enforce/)).toBeVisible();
});
it("matches the bounded marketplace grammar", () => {
  for (const s of ["org/repo", "codex:openai-curated-remote", "o/.github"]) expect(validMarketplace(s)).toBe(true);
  for (const s of ["-o/repo", "o/..", "codex:-flag", "codex:x/y", "*"]) expect(validMarketplace(s)).toBe(false);
});
