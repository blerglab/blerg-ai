import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { page } from "vitest/browser";
import { MetricsModal } from "./MetricsPage";
import "./styles.css";

const sessions = Array.from({ length: 30 }, (_, i) => ({ day: `2026-10-${String(i + 1).padStart(2, "0")}`, role: "worker", count: i + 1 }));
let rows = sessions;
vi.mock("./api", () => ({ api: vi.fn(async (path: string) => path.endsWith("/metrics")
  ? { sessions: rows, tokens_by_model: [], cards_completed: [], gate: [], rollup: { sessions: 30, agent_seconds: 0, tokens_in: 0, tokens_out: 0, cards_completed: 0, merges: 0, gate_calls: 0 } }
  : { name: "Autumn release" }) }));
afterEach(() => { cleanup(); rows = sessions; });

for (const width of [390, 3840]) it(`keeps two adjacent date labels apart at ${width}px`, async () => {
  await page.viewport(width, 1000);
  rows = sessions.slice(0, 2);
  render(<MetricsModal boardId="release" onClose={() => {}} />);
  const chart = await screen.findByRole("img", { name: "Workspaces per day" });
  const labels = [...chart.querySelectorAll<SVGTextElement>(".axis-label")].filter(label => label.textContent?.includes("-"));
  expect(labels).toHaveLength(2);
  expect(labels[0].getBoundingClientRect().right).toBeLessThanOrEqual(labels[1].getBoundingClientRect().left);
});

for (const width of [390, 3840]) it(`keeps a month of metrics and readable axis labels inside the sheet at ${width}px`, async () => {
  await page.viewport(width, 1000);
  render(<MetricsModal boardId="release" onClose={() => {}} />);
  const chart = await screen.findByRole("img", { name: "Workspaces per day" });
  const plot = chart.parentElement!;
  await waitFor(() => expect(plot.scrollWidth).toBeLessThanOrEqual(plot.clientWidth));
  expect(chart.getBoundingClientRect().width).toBeLessThanOrEqual(plot.clientWidth);
  expect(chart.getBoundingClientRect().height).toBeCloseTo(146, 2);
  expect(chart.querySelectorAll("rect")).toHaveLength(30);
  const labels = [...chart.querySelectorAll<SVGTextElement>(".axis-label")].filter(label => label.textContent?.includes("-"));
  expect(labels[0]).toHaveTextContent("10-01");
  expect(labels.at(-1)).toHaveTextContent("10-30");
  for (const label of labels) expect(parseFloat(getComputedStyle(label).fontSize)).toBeGreaterThanOrEqual(9.5);
  // Resizing an open sheet must recalculate bars without scaling its text.
  await page.viewport(width === 390 ? 3840 : 390, 1000);
  await waitFor(() => expect(chart.getBoundingClientRect().width).toBeLessThanOrEqual(plot.clientWidth));
});
