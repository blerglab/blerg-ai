import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import PluginsSection, { PLUGIN_ENGINES } from "./PluginsSection";
import Settings from "./Settings";
import * as authClient from "./authClient";

type Entry = { marketplace: string; plugin: string };
const OFFICIAL = "anthropics/claude-plugins-official";

// A tiny fake of core's GET/PUT /api/plugins/claude: PUT replaces the list unless `reject` is set
// (then it answers 422 with core's plain-text reason).
function fakeCore(initial: Entry[] = []) {
  const state = { plugins: initial, reject: "" as string, puts: [] as Entry[][], anyMarketplace: false };
  const body = () => ({
    engine: "claude",
    plugins: state.plugins,
    allowed_marketplaces: [OFFICIAL],
    any_marketplace: state.anyMarketplace,
    max: 20,
  });
  const fetchMock = vi.fn((url: string, init?: RequestInit) => {
    if (url === "/api/plugins/claude" && (init?.method ?? "GET") === "GET") {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body()) });
    }
    if (url === "/api/plugins/claude" && init?.method === "PUT") {
      const next = (JSON.parse(init.body as string) as { plugins: Entry[] }).plugins;
      state.puts.push(next);
      if (state.reject) {
        return Promise.resolve({ ok: false, status: 422, text: () => Promise.resolve(state.reject) });
      }
      state.plugins = next;
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body()) });
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) });
  });
  return { state, fetchMock };
}

describe("Always-on plugins", () => {
  let core: ReturnType<typeof fakeCore>;
  const setup = (initial: Entry[] = []) => {
    core = fakeCore(initial);
    vi.stubGlobal("fetch", core.fetchMock);
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

  it("explains what it does, and warns that plugins run code", async () => {
    setup();
    render(<PluginsSection />);
    await screen.findByText("No always-on plugins yet.");
    const help = document.querySelector(".section-help")!.textContent ?? "";
    expect(help).toMatch(/every cluster session started as you/);
    expect(help).toMatch(/automation token/);
    expect(help).toMatch(/do not change Claude Code on your own machine/);
    expect(help).toMatch(/run code in your sessions, so only add ones you trust/);
  });

  it("lists the plugins in order with remove buttons that PUT the rest", async () => {
    setup([
      { marketplace: OFFICIAL, plugin: "superpowers" },
      { marketplace: OFFICIAL, plugin: "frontend-design" },
    ]);
    render(<PluginsSection />);
    const rows = await screen.findAllByRole("row");
    expect(rows[1]).toHaveTextContent("superpowers");
    expect(rows[2]).toHaveTextContent("frontend-design");

    fireEvent.click(screen.getByRole("button", { name: "Remove superpowers" }));
    await waitFor(() => expect(screen.queryByText("superpowers")).not.toBeInTheDocument());
    expect(core.state.puts).toEqual([[{ marketplace: OFFICIAL, plugin: "frontend-design" }]]);
  });

  it("offers one-click suggestions from a data table, and disables one already added", async () => {
    setup();
    render(<PluginsSection />);
    await screen.findByText("No always-on plugins yet.");
    // One button per suggestion in the table — nothing hardcoded beyond it.
    for (const s of PLUGIN_ENGINES[0].suggestions) {
      expect(screen.getByRole("button", { name: `Add ${s.plugin}` })).toBeInTheDocument();
    }
    fireEvent.click(screen.getByRole("button", { name: "Add frontend-design" }));
    await screen.findByRole("button", { name: "frontend-design added" });
    expect(screen.getByRole("button", { name: "frontend-design added" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Add superpowers" }));
    await screen.findByRole("button", { name: "superpowers added" });
    expect(core.state.puts.at(-1)).toEqual([
      { marketplace: OFFICIAL, plugin: "frontend-design" },
      { marketplace: OFFICIAL, plugin: "superpowers" },
    ]);
  });

  it("adds a plugin from the form and clears the name", async () => {
    setup();
    render(<PluginsSection />);
    await screen.findByText("No always-on plugins yet.");
    const add = screen.getByRole("button", { name: "Add plugin" });
    expect(add).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/Plugin name/), { target: { value: " my-plugin " } });
    fireEvent.click(add);
    await waitFor(() => expect(screen.getByRole("table")).toHaveTextContent("my-plugin"));
    expect(core.state.puts[0]).toEqual([{ marketplace: OFFICIAL, plugin: "my-plugin" }]);
    expect(screen.getByLabelText(/Plugin name/)).toHaveValue("");
  });

  it("shows the server's validation reason, including marketplace not allowed", async () => {
    setup();
    render(<PluginsSection />);
    await screen.findByText("No always-on plugins yet.");
    core.state.reject = "marketplace not allowed by this install: evil/plugins";
    fireEvent.change(screen.getByLabelText(/Marketplace/), { target: { value: "evil/plugins" } });
    fireEvent.change(screen.getByLabelText(/Plugin name/), { target: { value: "steal" } });
    fireEvent.click(screen.getByRole("button", { name: "Add plugin" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("marketplace not allowed by this install");
    // The rejected entry is not shown as added, and what the person typed is kept to fix.
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.getByLabelText(/Plugin name/)).toHaveValue("steal");
  });

  it("does not render a non-422 body as the reason", async () => {
    setup();
    render(<PluginsSection />);
    await screen.findByText("No always-on plugins yet.");
    core.fetchMock.mockImplementationOnce(() => Promise.resolve({ ok: false, status: 500, text: () => Promise.resolve("<b>stack trace</b>") }));
    fireEvent.click(screen.getByRole("button", { name: "Add superpowers" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Failed to save Claude Code plugins (500).");
    expect(alert).not.toHaveTextContent("stack trace");
  });

  it("says which marketplaces this install accepts", async () => {
    setup();
    render(<PluginsSection />);
    const note = await screen.findByText(/This install accepts these marketplaces/);
    expect(note).toHaveTextContent(OFFICIAL);
    expect(note).toHaveTextContent("Up to 20 plugins");
  });

  it("is part of the Settings page", async () => {
    setup();
    render(<Settings />);
    const heading = await screen.findByRole("heading", { name: "Always-on plugins" });
    expect(within(heading.closest("section")!).getByText("Claude Code")).toBeInTheDocument();
  });
});
