import react from "@vitejs/plugin-react";
import { defineConfig } from "vite-plus";

import { shell } from "./build/shell";
import { lint } from "./lint/config";

export default defineConfig({
  plugins: [react(), shell()],
  build: { outDir: "../internal/assets/dist", emptyOutDir: true },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": "http://127.0.0.1:8080",
      "/ws": { target: "ws://127.0.0.1:8080", ws: true },
    },
  },
  fmt: {
    ignorePatterns: ["src/fixtures/**", "src/generated/**", "pnpm-lock.yaml", "**/*.md"],
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
  },
});
