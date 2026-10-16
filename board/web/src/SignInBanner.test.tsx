import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";

// The runner's twin: runner/frontend/src/components/SignInBanner.test.tsx.
const { redirectToRefresh, getAccessToken } = vi.hoisted(() => ({
  redirectToRefresh: vi.fn(),
  getAccessToken: vi.fn<() => string | null>(),
}));

vi.mock("./authClient", () => ({ redirectToRefresh, getAccessToken }));

import SignInBanner from "./SignInBanner";
import { SIGN_IN_GRACE_MS, bounceToRefresh, clearSignInStuck, markSignInStuck } from "./signIn";
import { MAX_ATTEMPTS, markRefreshAttempted, refreshAlreadyAttempted, refreshReturnUrl } from "./refreshGuard";

describe("SignInBanner", () => {
  beforeEach(() => {
    redirectToRefresh.mockClear();
    getAccessToken.mockReturnValue(null);
    sessionStorage.clear();
    act(() => clearSignInStuck());
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows nothing while signing in is still possible", () => {
    render(<SignInBanner />);
    expect(screen.queryByTestId("sign-in-banner")).not.toBeInTheDocument();
  });

  it("appears when a bounce is refused by the guard, and Retry clears both keys and bounces again", () => {
    for (let i = 0; i < MAX_ATTEMPTS; i++) markRefreshAttempted();
    render(<SignInBanner />);

    act(() => {
      expect(bounceToRefresh()).toBe(false);
    });
    expect(redirectToRefresh).not.toHaveBeenCalled();
    expect(screen.getByTestId("sign-in-banner")).toHaveTextContent("Couldn't sign you in");

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(redirectToRefresh).toHaveBeenCalledTimes(1);
    expect(redirectToRefresh).toHaveBeenCalledWith(refreshReturnUrl());
    // The retry is itself one counted attempt on a fresh window, so its return leg stays bounded.
    expect(refreshAlreadyAttempted()).toBe(true);
    expect(JSON.parse(sessionStorage.getItem("blerg_refresh_attempts")!).n).toBe(1);
  });

  it("appears when, ten seconds after load, there is still no token", () => {
    vi.useFakeTimers();
    render(<SignInBanner />);
    act(() => {
      vi.advanceTimersByTime(SIGN_IN_GRACE_MS - 1);
    });
    expect(screen.queryByTestId("sign-in-banner")).not.toBeInTheDocument();
    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(screen.getByTestId("sign-in-banner")).toBeInTheDocument();
  });

  it("stays hidden after ten seconds when a token is held", () => {
    vi.useFakeTimers();
    getAccessToken.mockReturnValue("tok");
    render(<SignInBanner />);
    act(() => {
      vi.advanceTimersByTime(SIGN_IN_GRACE_MS);
    });
    expect(screen.queryByTestId("sign-in-banner")).not.toBeInTheDocument();
  });

  it("goes away when told a socket opened", () => {
    render(<SignInBanner />);
    act(() => markSignInStuck());
    expect(screen.getByTestId("sign-in-banner")).toBeInTheDocument();
    act(() => clearSignInStuck());
    expect(screen.queryByTestId("sign-in-banner")).not.toBeInTheDocument();
  });
});
