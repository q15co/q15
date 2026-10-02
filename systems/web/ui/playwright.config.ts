import { defineConfig } from "@playwright/test";

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
  webServer: {
    command: "pnpm preview --port 4173",
    url: "http://127.0.0.1:4173",
    reuseExistingServer: process.env.CI === undefined,
  },
});
