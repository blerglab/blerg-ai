import { describe, it, expect } from "vitest";
import { commands } from "vitest/browser";
import { ensureFreshToken } from "./authClient";

// BLG-487: a tab queued behind another tab's lingering refresh lock used to wait lock-wait +
// SILENT_TIMEOUT_MS (up to ~135 s with a 120 s linger) before falling back to sign-in. The fix
// bounds the caller's wait — lock plus frame together — to SILENT_TIMEOUT_MS (~15 s). A mocked
// Web Locks matrix proves the logic, but only a real browser proves the real Web Locks API
// actually honors the AbortSignal the way the fix relies on. This opens a second real page on
// the dev server's own origin ("page A") to hold the shared lock, then drives the module under
// test as "page B".
describe("silent refresh: the caller's bound covers the shared lock wait too (real browser)", () => {
  it("falls back within ~15s while another page's frame lingers on the refresh lock", async () => {
    await commands.openLockHolderPage();
    try {
      const start = performance.now();
      const outcome = await ensureFreshToken();
      const elapsedMs = performance.now() - start;

      expect(outcome).toBe(false);
      // SILENT_TIMEOUT_MS is 15_000; generous slack either side for CI jitter, but tight enough
      // to catch a regression back to the old lock-wait + 15s (which would be >100s here since
      // the holder page never releases until the finally block below).
      expect(elapsedMs).toBeGreaterThan(10_000);
      expect(elapsedMs).toBeLessThan(25_000);
    } finally {
      await commands.releaseLockHolderPage();
    }
  }, 30_000);
});
