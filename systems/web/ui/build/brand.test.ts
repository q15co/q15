import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { expect, it } from "vite-plus/test";

import { checkBrand } from "./brand";
import assets from "./brand-assets.json";

it("accepts artwork generated from the current brand sources", () => {
  expect(() => checkBrand(resolve("."))).not.toThrow();
});

it.each([
  "brand/svg/favicon.svg",
  "systems/web/ui/public/icon.svg",
  "systems/web/ui/public/icon-192.png",
])("rejects stale artwork at %s with a regeneration command", (changed) => {
  const directory = mkdtempSync(join(tmpdir(), "q15-brand-"));
  const root = join(directory, "systems/web/ui");
  try {
    for (const asset of assets) {
      for (const file of [`../../../brand/svg/${asset.source}`, asset.target]) {
        const target = resolve(root, file);
        mkdirSync(dirname(target), { recursive: true });
        copyFileSync(resolve(file), target);
      }
    }
    writeFileSync(join(directory, changed), "stale artwork");
    expect(() => checkBrand(root)).toThrow(/Run make ui-assets/u);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});
