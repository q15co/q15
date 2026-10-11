import { describe, expect, it, vi } from "vite-plus/test";

import { decode, encode } from "./base64";

const decodeLegacy = (value: string) =>
  Uint8Array.from(
    atob(value.replaceAll("-", "+").replaceAll("_", "/")),
    (c) => c.codePointAt(0) ?? 0,
  );
const encodeLegacy = (bytes: Uint8Array) =>
  btoa(String.fromCodePoint(...bytes))
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replaceAll("=", "");
describe.each(["native", "fallback"])("%s base64url codec", (mode) => {
  it("preserves bytes and canonical encoding across chunk lengths", () => {
    if (mode === "native") {
      vi.stubGlobal(
        "Uint8Array",
        class extends Uint8Array {
          static fromBase64(value: string, _options: unknown) {
            return new this(decodeLegacy(value));
          }
          toBase64(_options: unknown) {
            return encodeLegacy(this);
          }
        },
      );
    }
    for (const size of [0, 1, 2, 3, 16, 65, 32768, 32784]) {
      const bytes = new Uint8Array(size);
      for (let index = 0; index < size; index++) bytes[index] = index % 256;
      const wire = encode(bytes);
      expect(wire).toBe(encodeLegacy(bytes));
      expect(decode(wire)).toEqual(bytes);
    }
    for (const value of ["Zg=", "Zh", "Zg ", "Z", "a+", "a/"])
      expect(() => decode(value)).toThrow(/.+/u);
    vi.unstubAllGlobals();
  });
});
