import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import SessionStorageSection from "./SessionStorageSection";

vi.mock("./apiFetch", () => ({ apiFetch: (...args: unknown[]) => fetch(...args as Parameters<typeof fetch>) }));
afterEach(() => vi.unstubAllGlobals());
const view = { default_upload_mb: 500, max_upload_mb: 5000, defaults: { default_upload_mb: 500, max_upload_mb: 5000 }, ranges: { default_upload_mb: [1, 1048576], max_upload_mb: [1, 1048576] }, step_mb: 500, custom: false };

describe("Session storage", () => {
  it("shows the default, the maximum and the step, and saves both", async () => {
    const mock = vi.fn((_url: string, init?: RequestInit) => Promise.resolve(new Response(JSON.stringify(init?.method === "PUT" ? { ...view, ...JSON.parse(init.body as string), custom: true } : view))));
    vi.stubGlobal("fetch", mock);
    render(<SessionStorageSection />);
    const def = await screen.findByLabelText("Attachments per session (MB)");
    const max = screen.getByLabelText("Most one session can be raised to (MB)");
    expect(def).toHaveValue(500);
    expect(max).toHaveValue(5000);
    expect(screen.getByText(/adds 500 MB/)).toHaveTextContent("BLERG_RUNNER_MAX_SESSION_UPLOADS_MB");
    fireEvent.change(def, { target: { value: "800" } });
    fireEvent.change(max, { target: { value: "3000" } });
    fireEvent.click(screen.getByRole("button", { name: "Save session storage" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Saved");
    expect(mock).toHaveBeenLastCalledWith("/api/settings/session-storage", expect.objectContaining({ method: "PUT", body: JSON.stringify({ default_upload_mb: 800, max_upload_mb: 3000 }) }));
    expect(screen.getByText(/recorded with who made them/)).toBeInTheDocument();
  });
  it("shows a refusal without claiming it was saved", async () => {
    vi.stubGlobal("fetch", vi.fn((_url: string, init?: RequestInit) => Promise.resolve(init?.method === "PUT" ? new Response("max_upload_mb (400) must be at least default_upload_mb (500)", { status: 400 }) : new Response(JSON.stringify(view)))));
    render(<SessionStorageSection />);
    await screen.findByLabelText("Attachments per session (MB)");
    fireEvent.click(screen.getByRole("button", { name: "Save session storage" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("must be at least");
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});
