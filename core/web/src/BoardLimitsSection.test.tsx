import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import BoardLimitsSection, { headroomLine } from "./BoardLimitsSection";
import Settings from "./Settings";
import * as authClient from "./authClient";

const DEFAULTS = {
  calls_per_minute: 600,
  calls_in_flight: 8,
  unrevoked_per_account: 20,
  live_exchange_tokens: 100,
};
const RANGES = {
  calls_per_minute: [1, 10000],
  calls_in_flight: [1, 64],
  unrevoked_per_account: [1, 1000],
  live_exchange_tokens: [10, 10000],
};

function fakeCore() {
  const state = { limits: { ...DEFAULTS }, puts: [] as unknown[], reject: "" };
  const view = () => ({
    ...state.limits,
    defaults: DEFAULTS,
    ranges: RANGES,
    custom: false,
    updated_at: null,
    runner_refresh_seconds: 60,
  });
  const fetchMock = vi.fn((url: string, init?: RequestInit) => {
    if (
      url === "/api/settings/board-limits" &&
      (init?.method ?? "GET") === "GET"
    ) {
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve(view()),
      });
    }
    if (url === "/api/settings/board-limits" && init?.method === "PUT") {
      const body = JSON.parse(init.body as string);
      state.puts.push(body);
      if (state.reject)
        return Promise.resolve({
          ok: false,
          status: 400,
          text: () => Promise.resolve(state.reject),
        });
      state.limits = body;
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve(view()),
      });
    }
    return Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve({}),
    });
  });
  return { state, fetchMock };
}

// A token whose caps claim is caps (unsigned: UI routing only).
const tokenWith = (caps: string[]) => `x.${btoa(JSON.stringify({ caps }))}.y`;

describe("Board call limits", () => {
  let core: ReturnType<typeof fakeCore>;
  beforeEach(() => {
    core = fakeCore();
    vi.stubGlobal("fetch", core.fetchMock);
    vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-access-token");
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("shows the limits with their defaults and saves all four", async () => {
    render(<BoardLimitsSection />);
    const perMinute = (await screen.findByLabelText(
      "Board requests a minute, per session",
    )) as HTMLInputElement;
    expect(perMinute.value).toBe("600");
    expect(
      screen.getByLabelText("Board calls at once, per session"),
    ).toHaveValue(8);
    expect(screen.getByText(/Default 100; 10 to 10000/)).toBeInTheDocument();
    expect(
      screen.getByText(/runners use the other limits within 60 seconds/i),
    ).toBeInTheDocument();
    fireEvent.change(perMinute, { target: { value: "1200" } });
    fireEvent.click(screen.getByRole("button", { name: "Save limits" }));
    await screen.findByText(
      /Saved\. Runners use the new limits within 60 seconds/,
    );
    expect(core.state.puts).toEqual([{ ...DEFAULTS, calls_per_minute: 1200 }]);
  });

  it("says how many busy sessions use up the account's live tokens", async () => {
    expect(headroomLine(64, 100)).toContain(
      "at 64 calls at once, 2 busy sessions of one account use all 100",
    );
    expect(headroomLine(8, 100)).toContain("13 busy sessions");
    expect(headroomLine(0, 100)).toBeNull();
    render(<BoardLimitsSection />);
    const inFlight = await screen.findByLabelText(
      "Board calls at once, per session",
    );
    expect(screen.getByTestId("board-limits-headroom")).toHaveTextContent(
      "at 8 calls at once, 13 busy sessions",
    );
    fireEvent.change(inFlight, { target: { value: "64" } });
    expect(screen.getByTestId("board-limits-headroom")).toHaveTextContent(
      "at 64 calls at once, 2 busy sessions of one account use all 100",
    );
  });

  it("shows core's reason when it refuses the values", async () => {
    core.state.reject =
      "unrevoked_per_account must be below live_exchange_tokens";
    render(<BoardLimitsSection />);
    await screen.findByLabelText("Unrevoked credentials per account");
    fireEvent.click(screen.getByRole("button", { name: "Save limits" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "unrevoked_per_account must be below live_exchange_tokens",
    );
  });

  it("is in Settings for an administrator only", async () => {
    vi.mocked(authClient.getAccessToken).mockReturnValue(
      tokenWith(["card.read"]),
    );
    const { unmount } = render(
      <MemoryRouter>
        <Settings />
      </MemoryRouter>,
    );
    await waitFor(() => expect(core.fetchMock).toHaveBeenCalled());
    expect(screen.queryByTestId("board-limits")).not.toBeInTheDocument();
    unmount();
    vi.mocked(authClient.getAccessToken).mockReturnValue(
      tokenWith(["card.read", "account.manage"]),
    );
    render(
      <MemoryRouter>
        <Settings />
      </MemoryRouter>,
    );
    expect(await screen.findByTestId("board-limits")).toBeInTheDocument();
  });
});
