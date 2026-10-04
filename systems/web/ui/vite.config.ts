import babel from "@rolldown/plugin-babel";
import react, { reactCompilerPreset } from "@vitejs/plugin-react";
import { defineConfig } from "vite-plus";

import { shell } from "./build/shell";
import { lint } from "./lint/config";

export default defineConfig({
  plugins: [react(), babel({ presets: [reactCompilerPreset({ target: "19" })] }), shell()],
  build: { outDir: "../internal/assets/dist", emptyOutDir: true },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/auth": "http://127.0.0.1:8080",
      "/api": "http://127.0.0.1:8080",
      "/ws": { target: "ws://127.0.0.1:8080", ws: true },
    },
  },
  fmt: {
    ignorePatterns: ["src/generated/**", "pnpm-lock.yaml", "**/*.md"],
    printWidth: 100,
    sortImports: {
      groups: [
        "type-import",
        ["value-builtin", "value-external"],
        ["type-parent", "type-sibling", "type-index"],
        ["value-parent", "value-sibling", "value-index"],
        "style",
        "unknown",
      ],
      newlinesBetween: true,
      sortSideEffects: false,
    },
  },
  lint,
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}", "build/**/*.test.ts", "lint/**/*.test.ts"],
    restoreMocks: true,
    coverage: {
      provider: "v8",
      include: ["src/**/*.{ts,tsx}", "build/**/*.ts", "worker/**/*.ts"],
      exclude: ["**/*.test.{ts,tsx}", "src/generated/**", "src/testing/**"],
      reporter: ["text", "html", "lcov", "json", "json-summary"],
      reportsDirectory: "coverage",
      reportOnFailure: true,
      thresholds: {
        lines: 98,
        statements: 97,
        functions: 95,
        branches: 90,
        perFile: { lines: 90, statements: 90, functions: 90, branches: 80 },
        "src/{application,domain,infrastructure,shared}/**/*.ts": {
          perFile: true,
          lines: 95,
          statements: 95,
          functions: 90,
          branches: 85,
        },
        "src/domain/**/*.ts": {
          perFile: true,
          lines: 98,
          statements: 95,
          functions: 100,
          branches: 95,
        },
        "worker/**/*.ts": { perFile: true, 100: true },
      },
    },
  },
});
