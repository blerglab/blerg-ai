import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import ChildLimitsSection from "./ChildLimitsSection";

vi.mock("./apiFetch", () => ({ apiFetch: (...args: unknown[]) => fetch(...args as Parameters<typeof fetch>) }));
afterEach(() => vi.unstubAllGlobals());
const view = { parallel: 3, starts_per_hour: 120, defaults: { parallel: 3, starts_per_hour: 120 }, ranges: { parallel: [1,50], starts_per_hour: [1,10000] } };

describe("Child session limits", () => {
  it("shows defaults and ranges and saves both fields", async () => {
    const mock = vi.fn((_url: string, init?: RequestInit) => Promise.resolve(new Response(JSON.stringify(init?.method === "PUT" ? { ...view, ...JSON.parse(init.body as string) } : view))));
    vi.stubGlobal("fetch", mock);
    render(<ChildLimitsSection />);
    const parallel = await screen.findByLabelText("Child sessions at once, per parent");
    const hourly = screen.getByLabelText("Child starts per rolling hour, per parent");
    expect(parallel).toHaveValue(3);
    expect(hourly).toHaveValue(120);
    expect(parallel).toHaveAttribute("max", "50");
    expect(hourly).toHaveAttribute("max", "10000");
    fireEvent.change(parallel, { target: { value: "8" } });
    fireEvent.change(hourly, { target: { value: "240" } });
    fireEvent.click(screen.getByRole("button", { name: "Save child limits" }));
    expect(await screen.findByRole("status")).toHaveTextContent("next start request");
    expect(mock).toHaveBeenLastCalledWith("/api/settings/child-limits", expect.objectContaining({ method: "PUT", body: JSON.stringify({ parallel: 8, starts_per_hour: 240 }) }));
    fireEvent.click(screen.getByRole("button", { name: "Use the defaults" }));
    expect(parallel).toHaveValue(3);
    expect(hourly).toHaveValue(120);
  });
  it("shows a save refusal without claiming it was saved", async () => {
    vi.stubGlobal("fetch", vi.fn((_url: string, init?: RequestInit) => Promise.resolve(init?.method === "PUT" ? new Response("parallel must be between 1 and 50", { status: 400 }) : new Response(JSON.stringify(view)))));
    render(<ChildLimitsSection />);
    await screen.findByLabelText("Child sessions at once, per parent");
    fireEvent.click(screen.getByRole("button", { name: "Save child limits" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("parallel must be between 1 and 50");
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});
