interface NativeBytes extends Uint8Array {
  toBase64(options: { alphabet: "base64url"; omitPadding: true }): string;
}
interface NativeDecoder extends Uint8ArrayConstructor {
  fromBase64(value: string, options: { alphabet: "base64url" }): Uint8Array<ArrayBuffer>;
}
const nativeBytes = (bytes: Uint8Array): bytes is NativeBytes =>
  "toBase64" in bytes && typeof bytes.toBase64 === "function";
const nativeDecoder = (constructor: Uint8ArrayConstructor): constructor is NativeDecoder =>
  "fromBase64" in constructor && typeof constructor.fromBase64 === "function";

export function encode(bytes: Uint8Array): string {
  if (nativeBytes(bytes)) return bytes.toBase64({ alphabet: "base64url", omitPadding: true });
  return btoa(String.fromCodePoint(...bytes))
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replaceAll("=", "");
}

export function decode(value: string): Uint8Array<ArrayBuffer> {
  if (!/^[\w-]*$/u.test(value)) throw new Error("Invalid base64url content.");
  const bytes = nativeDecoder(Uint8Array)
    ? Uint8Array.fromBase64(value, { alphabet: "base64url" })
    : Uint8Array.from(
        atob(value.replaceAll("-", "+").replaceAll("_", "/")),
        (c) => c.codePointAt(0) ?? 0,
      );
  // Native decoders also accept noncanonical trailing bits; the wire format does not.
  if (encode(bytes) !== value) throw new Error("Noncanonical base64url content.");
  return bytes;
}
