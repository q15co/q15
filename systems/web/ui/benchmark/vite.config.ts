import { resolve } from "node:path";
import { defineConfig } from "vite-plus";

import app from "../vite.config";

export default defineConfig({
  plugins: app.plugins?.slice(0, -1) ?? [],
  build: {
    outDir: "../benchmark-dist",
    emptyOutDir: true,
    target: "esnext",
    rolldownOptions: {
      input: {
        index: resolve(import.meta.dirname, "index.html"),
        codec: resolve(import.meta.dirname, "codec.html"),
      },
    },
  },
});
