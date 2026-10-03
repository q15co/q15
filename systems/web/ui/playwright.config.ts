import { defineConfig } from "@playwright/test";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

process.env.Q15_WEB_TEST_STATE_DIR ??= mkdtempSync(join(tmpdir(), "q15-auth-e2e-"));

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  use: {
    colorScheme: "dark",
    baseURL: "http://127.0.0.1:4173",
    viewport: { width: 1280, height: 900 },
    launchOptions:
      process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE === undefined
        ? {}
        : { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE },
  },
  webServer: [
    {
      command: "pnpm preview --port 4173",
      url: "http://127.0.0.1:4173",
      reuseExistingServer: process.env.CI === undefined,
    },
    {
      command: "make -C ../../.. ui-auth-test-server",
      url: "http://localhost:4184/healthz",
      reuseExistingServer: false,
      env: { Q15_WEB_TEST_STATE_DIR: process.env.Q15_WEB_TEST_STATE_DIR },
    },
  ],
});
