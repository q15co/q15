import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite-plus";
import { shell } from "./build/shell";

export default defineConfig({
  plugins: [react(), tailwindcss(), shell()],
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
    ignorePatterns: ["src/generated/**"],
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}", "build/**/*.test.ts"],
    restoreMocks: true,
  },
});
