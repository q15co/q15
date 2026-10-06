import { describe, expect, it } from "vite-plus/test";

import { baselineAdapter } from "../benchmark/baseline";

describe("benchmark baseline", () => {
  it("retains the selected revision's decoded-value validation", () => {
    const optimized =
      'import { parseFrameValue } from "../src/domain/protocol";\n' +
      'import { mainCodec } from "./base-codec";\n' +
      "export const codec = mainCodec(parseFrameValue);\n";
    expect(baselineAdapter(optimized)).toBe(optimized);
  });

  it("uses the legacy adapter only for revisions before the codec harness", () => {
    expect(baselineAdapter(undefined)).toBe('export { codec } from "./base-codec";\n');
    const legacy = 'export { codec } from "./base-codec";\n';
    expect(baselineAdapter(legacy)).toBe(legacy);
  });
});
