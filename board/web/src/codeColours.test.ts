import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { codeThemes } from "@blerglab/chat";

const api = vi.hoisted(() => vi.fn());
vi.mock("./api", () => ({ api }));

async function fresh() {
  vi.resetModules();
  return import("./codeColours");
}
const bg = () => document.documentElement.style.getPropertyValue("--chat-code-bg");

// Node's own (file-less) localStorage can shadow jsdom's in this runner: use a plain in-memory one.
function memoryStorage(): Storage {
  const m = new Map<string, string>();
  return {
    get length() { return m.size; },
    clear: () => m.clear(),
    getItem: (k) => m.get(k) ?? null,
    key: (i) => [...m.keys()][i] ?? null,
    removeItem: (k) => void m.delete(k),
    setItem: (k, v) => void m.set(k, String(v)),
  };
}

beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
  document.documentElement.removeAttribute("style");
  document.documentElement.setAttribute("data-theme", "dark");
  api.mockReset();
});
afterEach(() => {
  document.documentElement.removeAttribute("data-theme");
  vi.unstubAllGlobals();
});

describe("board code colours", () => {
  it("applies the choice the runner returns through the proxy, and remembers it", async () => {
    api.mockResolvedValue({ code_colours: { dark: "dracula", light: "blerg", overrides: { dark: { keyword: "#010203" }, light: {} } } });
    const m = await fresh();
    await m.loadCodeColours();
    expect(api).toHaveBeenCalledWith("/api/chat/preferences");
    expect(bg()).toBe(codeThemes.dracula.tokens.bg);
    expect(document.documentElement.style.getPropertyValue("--chat-code-keyword")).toBe("#010203");
    expect(JSON.parse(localStorage.getItem(m.CODE_COLOURS_KEY)!).dark).toBe("dracula");
  });

  it("paints the cached choice before the server answers, and keeps it if the server fails", async () => {
    localStorage.setItem("blerg-board.code-colours", JSON.stringify({ dark: "nord", light: "blerg", overrides: { dark: {}, light: {} } }));
    api.mockRejectedValue(new Error("offline"));
    const m = await fresh();
    m.applyCodeColoursToPage();
    expect(bg()).toBe(codeThemes.nord.tokens.bg);
    await m.loadCodeColours();
    expect(bg()).toBe(codeThemes.nord.tokens.bg);
  });

  it("falls back to Blerg when the runner has no choice, and ignores a malformed one", async () => {
    localStorage.setItem("blerg-board.code-colours", JSON.stringify({ dark: "nord", light: "blerg", overrides: { dark: {}, light: {} } }));
    api.mockResolvedValue({});
    let m = await fresh();
    await m.loadCodeColours();
    expect(bg()).toBe("var(--chat-bg)");
    api.mockResolvedValue({ code_colours: { dark: "<script>", light: 7, overrides: { dark: { keyword: "red" } } } });
    m = await fresh();
    await m.loadCodeColours();
    expect(bg()).toBe("var(--chat-bg)");
  });

  it("uses the light theme when the page is light", async () => {
    document.documentElement.setAttribute("data-theme", "light");
    api.mockResolvedValue({ code_colours: { dark: "nord", light: "github-light", overrides: { dark: {}, light: {} } } });
    const m = await fresh();
    await m.loadCodeColours();
    expect(bg()).toBe("#ffffff");
  });
});
