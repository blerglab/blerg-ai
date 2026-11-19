import type { Page } from "playwright";

// BLG-487: a second real page on the dev server's own origin, used by
// SilentRefreshLock.browser.test.ts to hold the shared navigator.locks refresh lock the way
// another tab's lingering silent-refresh frame would, so the test can prove the caller's bound
// covers the lock wait too, not just the frame's own wait.
const lockHolderPages = new WeakMap<object, Page>();

export async function openLockHolderPage({ page, context }: { page: Page; context: { newPage(): Promise<Page> } }): Promise<void> {
  const url = new URL("/src/test/silentRefreshLockHarness.html", page.url()).toString();
  const holder = await context.newPage();
  await holder.goto(url);
  await holder.evaluate(() => (window as unknown as { __lockHeld: Promise<void> }).__lockHeld);
  lockHolderPages.set(page, holder);
}

export async function releaseLockHolderPage({ page }: { page: Page }): Promise<void> {
  const holder = lockHolderPages.get(page);
  if (!holder) return;
  await holder.evaluate(() => (window as unknown as { __releaseLock: () => void }).__releaseLock());
  await holder.close();
  lockHolderPages.delete(page);
}
