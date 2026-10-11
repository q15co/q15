import { resolve } from "node:path";
import { defineConfig } from "vite-plus";

import app from "../vite.config";

export default defineConfig({
  plugins: [
    {
      name: "benchmark-work-counts",
      enforce: "pre",
      transform(code, id) {
        if (id.endsWith("/react-markdown/lib/index.js"))
          return code.replace(
            "return post(processor.runSync(processor.parse(file), file), options)",
            "const started = performance.now();\n" +
              "const result = post(processor.runSync(processor.parse(file), file), options);\n" +
              'performance.measure("q15-markdown", {start: started});\nreturn result',
          );
        if (id.endsWith("/src/components/message.tsx"))
          return code.replace(
            "const reduced = useMotionPreference();",
            'performance.mark(message.turn === "10001" ? "q15-live-render" : "q15-history-render");\nconst reduced = useMotionPreference();',
          );
        return null;
      },
    },
    ...(app.plugins?.slice(0, -1) ?? []),
  ],
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
