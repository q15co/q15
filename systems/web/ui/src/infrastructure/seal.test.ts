import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import vector from "../fixtures/protocol/sealed.json";
import { required } from "../testing/required";
import { sessionKey } from "../testing/session-key";
import { frame } from "./envelope";
import { saveSessionKey } from "./proof";
import { ContentSession, hasContent } from "./seal";

beforeEach(sessionKey);
afterEach(() => vi.unstubAllGlobals());

async function setup() {
  const source = await crypto.subtle.importKey(
    "jwk",
    vector.browser_key,
    { name: "ECDH", namedCurve: "P-256" },
    false,
    ["deriveKey"],
  );
  const publicKey = await crypto.subtle.importKey(
    "jwk",
    {
      kty: vector.browser_key.kty,
      crv: vector.browser_key.crv,
      x: vector.browser_key.x,
      y: vector.browser_key.y,
    },
    { name: "ECDH", namedCurve: "P-256" },
    true,
    [],
  );
  const signer = await sessionKey();
  await saveSessionKey(signer.key, vector.binding);
  vi.spyOn(crypto.subtle, "generateKey").mockResolvedValueOnce({ privateKey: source, publicKey });
  const content = new ContentSession();
  const hello = await content.offer("41");
  expect(source.extractable).toBe(false);
  await expect(crypto.subtle.exportKey("jwk", source)).rejects.toThrow(/.+/u);
  await content.accept({
    binding: hello.binding,
    public_key: vector.agent_public,
    channel_id: "channel",
  });
  return content;
}

describe("sealed browser content", () => {
  it("decrypts the agent's Go fixture and keeps metadata inside the envelope", async () => {
    const bits = vi.spyOn(crypto.subtle, "deriveBits");
    const keys = vi.spyOn(crypto.subtle, "deriveKey");
    const content = await setup();
    expect((await content.open(vector.frame)).payload).toEqual({
      code: "outbound",
      text: "sealed hello",
    });
    expect(await content.channel()).toBe("channel");
    const sealed = await content.wrap(
      frame("msg.send", { text: "private message", client_msg_id: "client" }),
    );
    const wire = JSON.stringify(sealed);
    expect(wire).not.toContain("private message");
    expect(wire).not.toContain("application/json");
    expect(wire).not.toContain("client_msg_id");
    await expect(content.open(vector.frame)).rejects.toThrow(/.+/u);
    for (const type of ["msg.send", "delta", "snapshot", "msg.final", "notice", "history"])
      expect(hasContent(type)).toBe(true);
    expect(hasContent("pong")).toBe(false);
    expect(bits).not.toHaveBeenCalled();
    for (const result of keys.mock.results) {
      if (result.type !== "return") throw new Error("Key derivation failed.");
      const key: unknown = await result.value;
      if (!(key instanceof CryptoKey)) throw new Error("Missing content key.");
      expect(key.extractable).toBe(false);
      await expect(crypto.subtle.exportKey("raw", key)).rejects.toThrow(/.+/u);
    }
  });

  it("rejects corruption, changed routing, truncation and reflected ciphertext", async () => {
    const chunk = required(vector.frame.payload.chunks[0]);
    for (const value of [
      { ...vector.frame, payload: null },
      { ...vector.frame, payload: { ...vector.frame.payload, version: 2 } },
      { ...vector.frame, payload: { ...vector.frame.payload, stream: "bad" } },
      { ...vector.frame, payload: { ...vector.frame.payload, stream: "!" } },
      { ...vector.frame, payload: { ...vector.frame.payload, chunks: [] } },
      { ...vector.frame, payload: { ...vector.frame.payload, chunks: [{ ...chunk, index: 1 }] } },
      {
        ...vector.frame,
        payload: { ...vector.frame.payload, chunks: [{ ...chunk, final: false }] },
      },
      {
        ...vector.frame,
        payload: { ...vector.frame.payload, chunks: [{ ...chunk, data: "AAAA" }] },
      },
      { ...vector.frame, payload: { ...vector.frame.payload, chunks: [{ ...chunk, data: "!" }] } },
      {
        ...vector.frame,
        payload: { ...vector.frame.payload, chunks: [{ ...chunk, data: "A".repeat(50000) }] },
      },
      { ...vector.frame, id: "other" },
    ]) {
      const content = await setup();
      await expect(content.open(value)).rejects.toThrow(/.+/u);
      expect((await content.open(vector.frame)).payload).toEqual({
        code: "outbound",
        text: "sealed hello",
      });
    }
    const content = await setup();
    await expect(
      content.open(await content.wrap(frame("notice", { text: "reflected" }))),
    ).rejects.toThrow(/.+/u);
  });

  it("frames large byte payloads with ordered chunks, including an exact boundary", async () => {
    const content = await setup();
    for (const size of [0, 32768 - 18, 32768, 100000]) {
      const sealed = await content.wrap(
        frame("msg.send", {}),
        "application/json",
        new Uint8Array(size),
      );
      const p = sealed.payload;
      if (typeof p !== "object" || p === null || !("chunks" in p) || !Array.isArray(p.chunks))
        throw new Error("Missing chunks");
      expect(p.chunks.length).toBe(Math.floor((size + 18) / 32768) + 1);
    }
    await expect(content.wrap(frame("msg.send", {}), "", new Uint8Array())).rejects.toThrow(/.+/u);
    await expect(
      content.wrap(frame("msg.send", {}), "x".repeat(1025), new Uint8Array()),
    ).rejects.toThrow(/.+/u);
    await expect(
      content.wrap(frame("msg.send", {}), "application/json", new Uint8Array(16 * 1024 * 1024 + 1)),
    ).rejects.toThrow(/.+/u);
  });

  it("drops keys on reconnect, rejects a wrong binding and cancels history waiters", async () => {
    const content = await setup();
    content.reset();
    await expect(content.open(vector.frame)).rejects.toThrow(/.+/u);
    await expect(content.wrap(frame("msg.send", {}))).rejects.toThrow(/.+/u);
    const waiting = content.channel();
    content.reset();
    await expect(waiting).rejects.toThrow(/.+/u);
    const controller = new AbortController();
    const pending = content.channel(controller.signal);
    controller.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    await expect(content.channel(controller.signal)).rejects.toMatchObject({ name: "AbortError" });
    await expect(content.accept(null)).rejects.toThrow(/.+/u);
    await content.offer("0");
    await expect(
      content.accept({ binding: "wrong", public_key: vector.agent_public, channel_id: "channel" }),
    ).rejects.toThrow(/.+/u);
    await expect(
      content.accept({ binding: vector.binding, public_key: "bad", channel_id: "channel" }),
    ).rejects.toThrow(/.+/u);
    await expect(
      content.accept({
        binding: vector.binding,
        public_key: vector.agent_public,
        channel_id: "x".repeat(129),
      }),
    ).rejects.toThrow(/.+/u);
    const offering = content.offer("0");
    content.reset();
    await expect(offering).rejects.toThrow(/.+/u);
  });
});
