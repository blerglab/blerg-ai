// Global test setup, wired via vite.config.ts `test.setupFiles`.
import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup, configure } from "@testing-library/react";

afterEach(() => {
  cleanup();
});

// Condition waits return immediately; only contention gets the extra budget.
configure({ asyncUtilTimeout: 15000 });
