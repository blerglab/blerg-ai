import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ManagedCredentials from "./ManagedCredentials";
import { apiFetch } from "./apiFetch";
vi.mock("./apiFetch", () => ({ apiFetch: vi.fn() }));
const fetchMock = vi.mocked(apiFetch);
const cap = { engine: "codex", kind: "codex_oauth", login: true, verification_origin: "https://auth.example.test" };
const pending = () => ({ attempt_id: "attempt-1", state: "pending", verification_url: "https://auth.example.test/device", user_code: "ABCD-EFGH", expires_at: new Date(Date.now() + 60_000).toISOString(), interval_seconds: 1 });
const response = (value: unknown) => ({ ok: true, status: 200, json: async () => value }) as Response;
async function settle() { await act(async () => { await Promise.resolve(); await Promise.resolve(); }); }
async function mount(onConnected = vi.fn()) {
  fetchMock.mockResolvedValueOnce(response([cap]));
  const view = render(<ManagedCredentials summaries={[]} labels={{ codex: "Codex" }} onConnected={onConnected} />);
  await settle(); return view;
}
beforeEach(() => { vi.useFakeTimers(); fetchMock.mockReset(); });
afterEach(() => { cleanup(); vi.useRealTimers(); });
describe("managed engine connections", () => {
  it("signs in and refreshes metadata without displaying credential material", async () => {
    const done = vi.fn(); await mount(done);
    fetchMock.mockResolvedValueOnce(response(pending()));
    fireEvent.click(screen.getByRole("button", { name: "Connect Codex" })); await settle();
    expect(screen.getByRole("link").getAttribute("href")).toBe("https://auth.example.test/device");
    expect(screen.getByText("ABCD-EFGH")).toBeTruthy();
    fetchMock.mockResolvedValueOnce(response({ attempt_id: "attempt-1", state: "connected" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(done).toHaveBeenCalledTimes(1); expect(screen.queryByText("ABCD-EFGH")).toBeNull();
    expect(fetchMock.mock.calls.every(([, init]) => !init?.body)).toBe(true);
  });
  it.each(["http://auth.example.test/device", "https://evil.example.test/device", "javascript:alert(1)", "https://user:password@auth.example.test/device", "https://auth.example.test/device#secret"])("rejects untrusted verification URL %s", async verification_url => {
    await mount(); fetchMock.mockResolvedValueOnce(response({ ...pending(), verification_url }));
    fireEvent.click(screen.getByRole("button", { name: "Connect Codex" })); await settle();
    expect(screen.queryByRole("link")).toBeNull(); expect(screen.getByRole("alert").textContent).toContain("Could not complete");
  });
  it("ignores a late poll result after cancellation", async () => {
    const done = vi.fn(); await mount(done); fetchMock.mockResolvedValueOnce(response(pending()));
    fireEvent.click(screen.getByRole("button", { name: "Connect Codex" })); await settle();
    let resolvePoll!: (value: Response) => void;
    fetchMock.mockImplementationOnce(() => new Promise(resolve => { resolvePoll = resolve; }));
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    fetchMock.mockResolvedValueOnce({ ok: true, status: 204 } as Response);
    fireEvent.click(screen.getByRole("button", { name: "Cancel connection" })); await settle();
    resolvePoll(response({ attempt_id: "attempt-1", state: "connected" })); await settle();
    expect(done).toHaveBeenCalledTimes(1); expect(screen.getByRole("button", { name: "Connect Codex" })).toBeTruthy();
    expect(fetchMock.mock.calls.at(-1)?.[1]?.method).toBe("DELETE");
  });
  it.each(["start", "poll", "body"])("recovers when %s hangs beyond the deadline", async stage => {
    await mount();
    if (stage === "poll") fetchMock.mockResolvedValueOnce(response(pending()));
    else if (stage === "body") fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: () => new Promise(() => {}) } as unknown as Response);
    else fetchMock.mockImplementationOnce(() => new Promise(() => {}));
    fireEvent.click(screen.getByRole("button", { name: "Connect Codex" })); await settle();
    if (stage === "poll") fetchMock.mockImplementationOnce(() => new Promise(() => {}));
    await act(async () => { await vi.advanceTimersByTimeAsync(13_100); });
    expect(screen.getByRole("alert").textContent).toContain("Could not complete");
    expect(screen.getByRole("button", { name: "Connect Codex" }).hasAttribute("disabled")).toBe(false);
  });
  it("refreshes an already committed connection after cancellation without deleting it", async () => {
    const done = vi.fn(); const view = await mount(done);
    done.mockImplementation(() => view.rerender(<ManagedCredentials summaries={[{ engine: "codex", kind: "codex_oauth", state: "usable", updated_at: "" }]} labels={{ codex: "Codex" }} onConnected={done} />));
    fetchMock.mockResolvedValueOnce(response(pending()));
    fireEvent.click(screen.getByRole("button", { name: "Connect Codex" })); await settle();
    fetchMock.mockResolvedValueOnce({ ok: true, status: 204 } as Response);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Cancel connection" })); await vi.advanceTimersByTimeAsync(0); });
    expect(screen.getByText("Codex: Connected")).toBeTruthy();
    expect(screen.queryByText("ABCD-EFGH")).toBeNull();
    const deletes = fetchMock.mock.calls.filter(([, init]) => init?.method === "DELETE");
    expect(deletes.map(([url]) => url)).toEqual(["/api/credentials/codex/managed/login/attempt-1"]);
  });
  it.each(["connected", "cancelled"])("bounds a hung metadata refresh after %s", async state => {
    const done = vi.fn(() => new Promise<void>(() => {})); await mount(done);
    fetchMock.mockResolvedValueOnce(response(state === "connected" ? { attempt_id: "attempt-1", state: "connected" } : pending()));
    fireEvent.click(screen.getByRole("button", { name: "Connect Codex" })); await settle();
    if (state === "cancelled") {
      fetchMock.mockResolvedValueOnce({ ok: true, status: 204 } as Response);
      fireEvent.click(screen.getByRole("button", { name: "Cancel connection" })); await settle();
    }
    await act(async () => { await vi.advanceTimersByTimeAsync(12_100); });
    expect(done).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: state === "connected" ? "Reconnect Codex" : "Connect Codex" }).hasAttribute("disabled")).toBe(false);
    expect(screen.getByRole("alert").textContent).toContain("status could not be refreshed");
    if (state === "connected") expect(screen.getByText("Codex: Connected")).toBeTruthy();
  });
  it("rejects a poll for another attempt and hides raw provider errors", async () => {
    await mount(); fetchMock.mockResolvedValueOnce(response(pending()));
    fireEvent.click(screen.getByRole("button", { name: "Connect Codex" })); await settle();
    fetchMock.mockResolvedValueOnce(response({ attempt_id: "foreign", state: "connected", error: "fixture-private-token" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(screen.getByRole("alert").textContent).not.toContain("fixture-private-token");
  });
  it("expires without sending another poll", async () => {
    await mount(); fetchMock.mockResolvedValueOnce(response({ ...pending(), expires_at: new Date(Date.now() + 500).toISOString() }));
    fireEvent.click(screen.getByRole("button", { name: "Connect Codex" })); await settle();
    await act(async () => { await vi.advanceTimersByTimeAsync(500); });
    expect(screen.getByRole("alert").textContent).toContain("expired"); expect(fetchMock).toHaveBeenCalledTimes(2);
  });
  it("uses current summaries after an opaque replacement", async () => {
    const view = await mount();
    view.rerender(<ManagedCredentials summaries={[{ engine: "codex", updated_at: "", kind: "codex_oauth", state: "usable" }]} labels={{ codex: "Codex" }} onConnected={vi.fn()} />);
    expect(screen.getByText("Codex: Connected")).toBeTruthy();
    view.rerender(<ManagedCredentials summaries={[{ engine: "codex", updated_at: "", kind: "opaque", state: "configured" }]} labels={{ codex: "Codex" }} onConnected={vi.fn()} />);
    expect(screen.getByText("Codex: Not connected")).toBeTruthy();
  });
});
