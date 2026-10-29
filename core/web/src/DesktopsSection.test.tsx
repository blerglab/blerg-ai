import { beforeEach, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import DesktopsSection, { daemonServerURL, enrollCommand } from "./DesktopsSection";

const mock = vi.hoisted(() => ({ fetch: vi.fn() }));
vi.mock("./apiFetch", () => ({ apiFetch: mock.fetch }));
const site = { version: "x", board_url: "", runner_url: "https://runner.example.test" };
const code = "bec_" + "a".repeat(40);
const json = (body: unknown, status = 200) => ({ ok: status < 300, status, json: async () => body });
const desktops = [
  { id: "d1", name: "Work laptop", host_name: "laptop-1", owner_id: "me", ownership_revision: 2, state: "enrolled", connected: true, last_seen_at: null, mine: true, can_enroll: false, can_rename: true, can_revoke: true },
  { id: "d2", name: "studio", host_name: "studio", owner_id: "me", ownership_revision: 1, state: "offline", connected: false, last_seen_at: "2026-10-25T10:00:00Z", mine: true, can_enroll: false, can_rename: true, can_revoke: true },
  { id: "d3", name: "old-box", host_name: "old-box", owner_id: "", ownership_revision: 4, state: "needs_enrolling", connected: true, last_seen_at: null, mine: false, can_enroll: true, can_rename: false, can_revoke: false },
];
let calls: { url: string; method: string; body?: unknown }[] = [];
beforeEach(() => {
  calls = [];
  mock.fetch.mockReset();
  mock.fetch.mockImplementation(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    calls.push({ url, method, body: init?.body ? JSON.parse(init.body as string) : undefined });
    if (url === "/api/desktops") return json({ desktops, owners: [], actor: "me", can_manage_others: false });
    if (url === "/api/desktop-enrollment-codes" && method === "GET") return json({ codes: [] });
    if (url === "/api/desktop-enrollment-codes" && method === "POST") return json({ id: "c1", code, expires_at: "2026-10-26T12:15:00Z" });
    if (url === "/api/desktops/d3/enrollment-connection") return json({ desktop_id: "d3", connection_generation: "g1", hostname: "old-box.local", os_user: "me", connected_since: "2026-10-26T11:00:00Z", source_address: "192.0.2.4:5000" });
    return json({ status: "ok" });
  });
});

it("builds the one enroll command from the runner address", () => {
  expect(daemonServerURL(site)).toBe("wss://runner.example.test/ws/daemon");
  expect(daemonServerURL({ ...site, runner_url: "http://localhost:8083/" })).toBe("ws://localhost:8083/ws/daemon");
  expect(enrollCommand(site, code)).toBe(`BLERG_RUNNER_SERVER_URL=wss://runner.example.test/ws/daemon install/desktop/daemon/install.sh install --enroll ${code}`);
});

it("lists each desktop with its state and last seen", async () => {
  render(<DesktopsSection site={site} />);
  const work = await screen.findByTestId("desktop-d1");
  expect(within(work).getByText("Enrolled")).toBeInTheDocument();
  expect(within(work).getByText(/Connected now/)).toBeInTheDocument();
  expect(within(work).getByText("laptop-1")).toBeInTheDocument();
  expect(within(screen.getByTestId("desktop-d2")).getByText("Offline")).toBeInTheDocument();
  expect(within(screen.getByTestId("desktop-d2")).getByText(/Last seen/)).toBeInTheDocument();
  const old = screen.getByTestId("desktop-d3");
  expect(within(old).getByText("Needs enrolling")).toBeInTheDocument();
  expect(within(old).queryByRole("button", { name: "Remove" })).toBeNull();
});

it("adds a desktop: shows the code once in one command, then forgets it", async () => {
  const writeText = vi.fn(async () => {});
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
  render(<DesktopsSection site={site} />);
  fireEvent.click(await screen.findByRole("button", { name: "Add a desktop" }));
  const command = await screen.findByTestId("enroll-command");
  expect(command.textContent).toBe(enrollCommand(site, code));
  fireEvent.click(screen.getByRole("button", { name: "Copy command" }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(enrollCommand(site, code)));
  fireEvent.click(screen.getByRole("button", { name: "Done" }));
  await waitFor(() => expect(screen.queryByTestId("enroll-command")).toBeNull());
  expect(document.body.textContent).not.toContain(code);
});

it("removes a desktop only after confirming", async () => {
  render(<DesktopsSection site={site} />);
  const work = await screen.findByTestId("desktop-d1");
  fireEvent.click(within(work).getByRole("button", { name: "Remove" }));
  expect(calls.some((c) => c.method === "DELETE")).toBe(false);
  fireEvent.click(within(work).getByRole("button", { name: "Remove desktop" }));
  await waitFor(() => expect(calls).toContainEqual({ url: "/api/desktops/d1", method: "DELETE", body: undefined }));
  expect(await screen.findByText(/was removed and its credential revoked/)).toBeInTheDocument();
});

it("renames a desktop", async () => {
  render(<DesktopsSection site={site} />);
  fireEvent.click(within(await screen.findByTestId("desktop-d2")).getByRole("button", { name: "Rename" }));
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Studio Mac" } });
  fireEvent.click(screen.getByRole("button", { name: "Save name" }));
  await waitFor(() => expect(calls).toContainEqual({ url: "/api/desktops/d2", method: "PATCH", body: { name: "Studio Mac" } }));
});

it("enrolls a connected desktop after the person confirms its connection details", async () => {
  render(<DesktopsSection site={site} />);
  fireEvent.click(within(await screen.findByTestId("desktop-d3")).getByRole("button", { name: "Enroll" }));
  const confirm = await screen.findByRole("region", { name: "Confirm desktop enrollment" });
  expect(within(confirm).getByText("old-box.local")).toBeInTheDocument();
  expect(within(confirm).getByText("192.0.2.4:5000")).toBeInTheDocument();
  expect(calls.some((c) => c.url.endsWith("/enroll"))).toBe(false);
  fireEvent.click(within(confirm).getByRole("button", { name: "Confirm enrollment" }));
  await waitFor(() => expect(calls).toContainEqual({ url: "/api/desktops/d3/enroll", method: "POST", body: { confirmed: true, connection_generation: "g1" } }));
  expect(await screen.findByText(/Enrollment requested/)).toBeInTheDocument();
});

it("says why the list is unavailable", async () => {
  mock.fetch.mockImplementation(async (url: string) => (url === "/api/desktops" ? json({ error: "Your desktops are unavailable: core cannot reach the runner (BLERG_CORE_RUNNER_URL)." }, 503) : json({ codes: [] })));
  render(<DesktopsSection site={site} />);
  expect(await screen.findByRole("alert")).toHaveTextContent("BLERG_CORE_RUNNER_URL");
});
