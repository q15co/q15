import { beforeEach, describe, expect, it, vi } from "vite-plus/test";

import vector from "../fixtures/protocol/sealed.json";
import { MaxMessageBytes, MaxClientFrameBytes, MaxServerFrameBytes } from "../generated/protocol";
import { sessionKey } from "../testing/session-key";
import { ContentEngine } from "./content-engine";
import { clientFrame, frame } from "./envelope";
import { ContentSession } from "./seal";

beforeEach(sessionKey);
async function setup() {
  const privateKey = await crypto.subtle.importKey(
    "jwk",
    vector.browser_key,
    { name: "ECDH", namedCurve: "P-256" },
    false,
    ["deriveKey"],
  );
  const { kty, crv, x, y } = vector.browser_key;
  const publicKey = await crypto.subtle.importKey(
    "jwk",
    { kty, crv, x, y },
    { name: "ECDH", namedCurve: "P-256" },
    true,
    [],
  );
  vi.spyOn(crypto.subtle, "generateKey").mockResolvedValueOnce({ privateKey, publicKey });
  const engine = new ContentEngine();
  const offer = await engine.execute({ op: "offer", cursor: "41", binding: vector.binding });
  expect(offer.kind === "wire" ? offer.data : "").toContain('"hello"');
  expect(privateKey.extractable).toBe(false);
  const key = await engine.execute({
    op: "receive",
    data: JSON.stringify(
      frame("key", {
        binding: vector.binding,
        public_key: vector.agent_public,
        channel_id: "channel",
      }),
    ),
  });
  expect(key).toEqual({ kind: "key", channel: "channel" });
  return engine;
}
describe("worker content engine", () => {
  it("opens authenticated content directly to a domain frame without a JSON round trip", async () => {
    const engine = await setup();
    const wire = JSON.stringify(vector.frame);
    const stringify = vi.spyOn(JSON, "stringify");
    expect(await engine.execute({ op: "receive", data: wire })).toMatchObject({
      kind: "frame",
      frame: { payload: { text: "sealed hello" } },
    });
    expect(stringify).not.toHaveBeenCalled();
    expect(await engine.execute({ op: "receive", data: wire })).toMatchObject({
      kind: "error",
      code: "unseal_failed",
    });
    expect(engine.needsRefresh).toBe(false);
  });
  it("never returns partially authenticated data and retains plaintext control frames", async () => {
    const engine = await setup();
    const damaged = { ...vector.frame, payload: { ...vector.frame.payload, chunks: [] } };
    expect(await engine.execute({ op: "receive", data: JSON.stringify(damaged) })).toMatchObject({
      kind: "error",
      code: "unseal_failed",
    });
    expect(
      await engine.execute({
        op: "receive",
        data: JSON.stringify(frame("ready", { cursor: "0", head_seq: "0", device_id: "device" })),
      }),
    ).toMatchObject({ kind: "frame", frame: { type: "ready" } });
    expect(
      await engine.execute({ op: "receive", data: JSON.stringify(vector.frame) }),
    ).toMatchObject({ kind: "frame" });
  });
  it("encodes outgoing content and checks both UTF-8 message and encoded wire limits", async () => {
    const engine = await setup();
    const result = await engine.execute({
      op: "send",
      frame: clientFrame("msg.send", { text: "private", client_msg_id: "one" }),
    });
    expect(result).toMatchObject({ kind: "wire" });
    expect(JSON.stringify(result)).not.toContain("private");
    const ping = await engine.execute({ op: "send", frame: clientFrame("ping", {}) });
    expect(ping.kind === "wire" ? ping.data : "").toContain('"ping"');
    expect(
      await engine.execute({
        op: "send",
        frame: clientFrame("msg.send", {
          text: "😀".repeat(MaxMessageBytes / 4 + 1),
          client_msg_id: "two",
        }),
      }),
    ).toMatchObject({ kind: "error", code: "message_too_large" });
    vi.spyOn(ContentSession.prototype, "wrap").mockResolvedValueOnce(
      frame("msg.send", { padding: "x".repeat(MaxClientFrameBytes) }),
    );
    expect(
      await engine.execute({
        op: "send",
        frame: clientFrame("msg.send", { text: "small", client_msg_id: "three" }),
      }),
    ).toMatchObject({ kind: "error", code: "message_too_large" });
  });
  it("bounds incoming wire bytes before parsing and validates history leases", async () => {
    const engine = new ContentEngine();
    expect(
      await engine.execute({ op: "receive", data: "x".repeat(MaxServerFrameBytes + 1) }),
    ).toMatchObject({ kind: "error", code: "content_too_large" });
    expect(
      await engine.execute({
        op: "history",
        data: new ArrayBuffer(MaxServerFrameBytes + 1),
        channel: "channel",
      }),
    ).toMatchObject({ kind: "error", code: "content_too_large" });
    await expect(
      engine.execute({ op: "history", data: new ArrayBuffer(0), channel: "channel" }),
    ).rejects.toThrow("retired");
    const live = await setup();
    await expect(
      live.execute({ op: "history", data: new ArrayBuffer(0), channel: "wrong" }),
    ).rejects.toThrow("retired");
    for (const wire of [frame("history", {}, "wrong"), frame("notice", {}, "channel")])
      await expect(
        live.execute({
          op: "history",
          data: new TextEncoder().encode(JSON.stringify(wire)).buffer,
          channel: "channel",
        }),
      ).rejects.toThrow("unsupported history");
    await expect(
      live.execute({ op: "history", data: new Uint8Array([255]).buffer, channel: "channel" }),
    ).rejects.toThrow(/.+/u);
  });
});
