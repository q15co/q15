import { defineConfig } from "vite-plus";

import app from "../vite.config";

export default defineConfig({
  plugins: app.plugins?.slice(0, -1) ?? [],
  build: { outDir: "../benchmark-dist", emptyOutDir: true, target: "esnext" },
});
