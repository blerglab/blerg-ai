import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { parseLaunchOptions, useBoardLaunchOptions } from "./launchOptions";
vi.mock("./api", () => ({ api: vi.fn() }));
const reply = (id = "codex") => ({ state: "known", engines: [{ id, name: id, models: { models: [{ id: "fixture-model", efforts: ["high"] }] } }] });
beforeEach(() => vi.mocked(api).mockReset());
describe("board scoped discovery", () => {
  it("rejects malformed identities and model catalogs", () => {
    for (const id of [undefined, null, 42, "../engine"]) expect(() => parseLaunchOptions({ state: "known", engines: [{ id, name: "bad", models: { models: [] } }] })).toThrow();
    expect(() => parseLaunchOptions({ state: "known", engines: [{ id: "fixture", name: "Fixture", models: { models: [{ id: "m", efforts: [3] }] } }] })).toThrow();
    expect(parseLaunchOptions(reply("future-engine")).engines[0].id).toBe("future-engine");
    expect(parseLaunchOptions({ state: "known", engines: [] }).engines).toEqual([]);
  });
  it("does not read catalogs until a stored automation identity is usable", async () => {
    renderHook(() => useBoardLaunchOptions("b1", false));
    expect(api).not.toHaveBeenCalled();
  });
  it("hides stale choices and ignores a late prior-board response", async () => {
    let finish: (value: unknown) => void = () => {};
    vi.mocked(api).mockImplementationOnce(() => new Promise(resolve => { finish = resolve; })).mockResolvedValue(reply("future-engine"));
    const { result, rerender } = renderHook(({ id }) => useBoardLaunchOptions(id), { initialProps: { id: "b1" } });
    rerender({ id: "b2" });
    expect(result.current.engines).toEqual([]);
    await waitFor(() => expect(result.current.state).toBe("known"));
    await act(async () => finish(reply("claude")));
    expect(result.current.engines[0].id).toBe("future-engine");
    expect(api).toHaveBeenLastCalledWith("/api/boards/b2/launch-options", expect.anything());
  });
  it("bounds hung authentication/fetch and retries explicitly", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(api).mockImplementation(() => new Promise(() => {}));
      const { result } = renderHook(() => useBoardLaunchOptions("b1"));
      await act(async () => { vi.advanceTimersByTime(8000); });
      expect(result.current.state).toBe("unknown");
      expect(result.current.error).toMatch(/timed out/);
      vi.mocked(api).mockResolvedValue(reply());
      await act(async () => { result.current.retry(); });
      expect(api).toHaveBeenLastCalledWith("/api/boards/b1/launch-options?refresh=1", expect.anything());
      expect(result.current.state).toBe("known");
    } finally { vi.useRealTimers(); }
  });
});
