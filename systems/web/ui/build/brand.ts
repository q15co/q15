import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import assets from "./brand-assets.json";

export function checkBrand(root: string) {
  for (const asset of assets) {
    const source = readFileSync(resolve(root, "../../../brand/svg", asset.source));
    const output = readFileSync(resolve(root, asset.target));
    if (
      createHash("sha256").update(source).digest("hex") !== asset.sourceHash ||
      createHash("sha256").update(output).digest("hex") !== asset.outputHash
    ) {
      throw new Error(`Stale web artwork: ${asset.target}. Run make ui-assets to regenerate it.`);
    }
  }
}
