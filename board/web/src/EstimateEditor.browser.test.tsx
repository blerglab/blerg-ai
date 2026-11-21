import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { page } from "vitest/browser";
import { EstimateEditor } from "./EstimateEditor";
import { api, type Card } from "./api";
import "./styles.css";
vi.mock("./api", () => ({ api: vi.fn() }));
afterEach(cleanup);
for (const width of [390, 3840]) {
  it(`edits estimates without overflow at ${width}px`, async () => {
    await page.viewport(width, 900);
    const card = { id: "c1", size: width === 390 ? "XS" : "M", risk: "high", complexity: "low", version: 3 } as Card;
    const onChange = vi.fn();
    vi.mocked(api).mockResolvedValue({ ...card, risk: "medium", version: 4 });
    render(<EstimateEditor card={card} onChange={onChange} />);
    expect(screen.getByRole("combobox", { name: "Size" })).toHaveValue(card.size);
    fireEvent.change(screen.getByRole("combobox", { name: "Risk" }), { target: { value: "medium" } });
    await waitFor(() => expect(onChange).toHaveBeenCalled());
    expect(api).toHaveBeenCalledWith("/api/cards/c1", { method: "PATCH", body: JSON.stringify({ risk: "medium", if_match: 3 }) });
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width);
  });
}
