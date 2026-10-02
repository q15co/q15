import react from "@vitejs/plugin-react";
import { defineConfig } from "vite-plus";
import { shell } from "./build/shell";

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
  },
  lint: {
    plugins: ["react", "typescript", "jsx-a11y", "vitest"],
    options: { typeAware: true, typeCheck: true },
    rules: {
      "typescript/no-explicit-any": "error",
      "typescript/no-unsafe-assignment": "error",
      "typescript/no-unsafe-argument": "error",
      "typescript/no-unsafe-call": "error",
      "typescript/no-unsafe-member-access": "error",
      "typescript/no-unsafe-return": "error",
      "typescript/no-unsafe-type-assertion": "error",
    },
    ignorePatterns: ["src/generated/**"],
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}", "build/**/*.test.ts"],
    restoreMocks: true,
  },
});
