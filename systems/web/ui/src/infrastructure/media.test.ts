import { Blob as NodeBlob, File as NodeFile } from "node:buffer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { MediaFile } from "../generated/protocol";

import fixture from "../fixtures/protocol/media.json";
import {
  MaxMediaBytes,
  MaxMediaFiles,
  MaxMediaHeaderBytes,
  MaxMediaPlainBytes,
  MaxMediaWireBytes,
} from "../generated/protocol";
import { TestSealer } from "../testing/content-peer";
import { required } from "../testing/required";
import { sessionKey } from "../testing/session-key";
import { frame } from "./envelope";
import { mediaAdapter } from "./media";
import { ContentSession } from "./seal";

const ref = required(fixture.parts[0]).media_ref;
const mediaType = "application/vnd.q15.media";
const createURL = vi.fn<(blob: Blob) => string>(() => "blob:decrypted");
const revokeURL = vi.fn<(url: string) => void>();

beforeEach(async () => {
  await sessionKey();
  vi.stubGlobal("Blob", NodeBlob);
  vi.stubGlobal("File", NodeFile);
  vi.stubGlobal("URL", { createObjectURL: createURL, revokeObjectURL: revokeURL });
  createURL.mockClear();
  revokeURL.mockClear();
});
afterEach(() => vi.unstubAllGlobals());

async function setup() {
  const content = new ContentSession();
  const hello = await content.offer("0");
  const peer = new TestSealer(hello);
  await content.accept(peer.key.payload);
  const fetch = vi.fn<typeof globalThis.fetch>();
  vi.stubGlobal("fetch", fetch);
  return { content, peer, fetch, media: mediaAdapter(content) };
}

function bundle(files: MediaFile[], bytes: Uint8Array) {
  const header = new TextEncoder().encode(JSON.stringify(files));
  const out = new Uint8Array(4 + header.length + bytes.length);
  new DataView(out.buffer).setUint32(0, header.length);
  out.set(header, 4);
  out.set(bytes, 4 + header.length);
  return Buffer.from(out);
}

function uploadResponse(peer: TestSealer, body: unknown) {
  if (typeof body !== "string") throw new Error("Expected sealed upload.");
  const value: unknown = JSON.parse(body);
  const wire = frame("media.put", {});
  if (
    typeof value !== "object" ||
    value === null ||
    !("id" in value) ||
    typeof value.id !== "string"
  )
    throw new Error("Invalid upload.");
  wire.id = value.id;
  return Response.json(
    peer.seal(frame("media.done", { parts: [required(fixture.parts[0])] }, wire.id)),
  );
}

describe("sealed media adapter", () => {
  it("uploads a batch through proof-carrying fetch with descriptors inside the envelope", async () => {
    const { content, peer, fetch, media } = await setup();
    const file = new File(["private file bytes"], "private.txt", { type: "text/plain" });
    fetch.mockImplementation((_url, init) => Promise.resolve(uploadResponse(peer, init?.body)));
    expect(await media.upload([file])).toEqual([required(fixture.parts[0])]);
    const [path, init] = required(fetch.mock.calls[0]);
    expect(path).toBe("/api/media");
    expect(init?.credentials).toBe("same-origin");
    expect(init?.cache).toBe("no-store");
    expect(init?.method).toBe("POST");
    const headers = new Headers(init?.headers);
    expect(headers.get("Q15-Proof")).toMatch(/.+/u);
    expect(headers.get("Q15-Channel")).toBe(await content.channel());
    expect(JSON.stringify(init?.body)).not.toContain("private.txt");
    expect(JSON.stringify(init?.body)).not.toContain("private file bytes");
  });

  it("authenticates a download before creating its object URL and releases it explicitly", async () => {
    const { peer, fetch, media } = await setup();
    const bytes = new TextEncoder().encode("<script>download only</script>");
    fetch.mockResolvedValue(
      Response.json(
        peer.seal(
          frame("media.get", {}, ref),
          mediaType,
          bundle(
            [{ filename: "unsafe.html", content_type: "text/html", size: bytes.length }],
            bytes,
          ),
        ),
      ),
    );
    const controller = new AbortController();
    const object = await media.load(ref, controller.signal);
    expect(object.filename).toBe("unsafe.html");
    expect(object.contentType).toBe("text/html");
    expect(object.url).toBe("blob:decrypted");
    expect(await required(createURL.mock.calls[0])[0].text()).toBe(
      "<script>download only</script>",
    );
    object.dispose();
    expect(revokeURL).toHaveBeenCalledWith("blob:decrypted");
    expect(required(fetch.mock.calls[0])[1]?.signal).toBe(controller.signal);
  });

  it.each([401, 413, 502])(
    "surfaces HTTP %i without interpreting error content",
    async (status) => {
      const { fetch, media } = await setup();
      fetch.mockResolvedValue(new Response("private diagnostics", { status }));
      await expect(media.upload([new File(["x"], "file")])).rejects.toThrow(
        status === 413 ? "8 MiB" : status === 401 ? "sign in" : "transfer failed",
      );
      expect(createURL).not.toHaveBeenCalled();
    },
  );

  it("rejects empty, excessive and oversized batches before network transfer", async () => {
    const { fetch, media } = await setup();
    await expect(media.upload([])).rejects.toThrow("1 and 16");
    await expect(
      media.upload(Array.from({ length: MaxMediaFiles + 1 }, () => new File([], "file"))),
    ).rejects.toThrow("1 and 16");
    await expect(
      media.upload([new File([new Uint8Array(MaxMediaBytes + 1)], "large")]),
    ).rejects.toThrow("8 MiB");
    await expect(media.upload([new File([], "x".repeat(MaxMediaHeaderBytes))])).rejects.toThrow(
      "names are too long",
    );
    expect(fetch).not.toHaveBeenCalled();
  });

  it("retains small images and downsizes large images to 2048px before upload", async () => {
    const { peer, fetch, media } = await setup();
    const close = vi.fn<() => void>();
    const bitmap = vi
      .fn<() => Promise<unknown>>()
      .mockResolvedValueOnce({ width: 100, height: 200, close })
      .mockResolvedValue({ width: 4096, height: 2048, close });
    vi.stubGlobal("createImageBitmap", bitmap);
    const draw = vi.fn<(...args: unknown[]) => void>();
    const canvas = {
      width: 0,
      height: 0,
      getContext: () => ({ drawImage: draw }),
      toBlob: (callback: (blob: Blob | null) => void, type: string) =>
        callback(new Blob(["resized"], { type })),
    };
    vi.stubGlobal("document", { createElement: () => canvas });
    fetch.mockImplementation((_url, init) => Promise.resolve(uploadResponse(peer, init?.body)));
    await media.upload([new File(["small"], "small.jpg", { type: "image/jpeg" })]);
    await media.upload([new File(["large"], "large.jpg", { type: "image/jpeg" })]);
    expect(canvas.width).toBe(2048);
    expect(canvas.height).toBe(1024);
    expect(draw).toHaveBeenCalledOnce();
    await media.upload([new File(["large"], "large.webp", { type: "image/webp" })]);
    expect(close).toHaveBeenCalledTimes(3);
  });

  it("reports image resize failures without uploading", async () => {
    const { fetch, media } = await setup();
    const close = vi.fn<() => void>();
    vi.stubGlobal("createImageBitmap", () => Promise.resolve({ width: 4096, height: 4096, close }));
    vi.stubGlobal("document", {
      createElement: () => ({ width: 0, height: 0, getContext: () => null }),
    });
    await expect(media.upload([new File([], "broken.png", { type: "image/png" })])).rejects.toThrow(
      "could not be resized",
    );
    vi.stubGlobal("document", {
      createElement: () => ({
        width: 0,
        height: 0,
        getContext: () => ({ drawImage: () => {} }),
        toBlob: (callback: (blob: Blob | null) => void) => callback(null),
      }),
    });
    await expect(media.upload([new File([], "broken.png", { type: "image/png" })])).rejects.toThrow(
      "could not be resized",
    );
    expect(close).toHaveBeenCalledTimes(2);
    expect(fetch).not.toHaveBeenCalled();
  });

  it("refuses an upload whose content channel changes before fetch", async () => {
    const { content, fetch, media } = await setup();
    vi.spyOn(content, "channel").mockResolvedValueOnce("first").mockResolvedValueOnce("second");
    await expect(media.upload([new File(["x"], "file")])).rejects.toThrow("Chat reconnected");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("rejects missing, huge and misrouted response bodies", async () => {
    const { peer, fetch, media } = await setup();
    fetch.mockResolvedValueOnce(new Response(null));
    await expect(media.load(ref, new AbortController().signal)).rejects.toThrow(
      "response is empty",
    );
    fetch.mockResolvedValueOnce(new Response(new Uint8Array(MaxMediaWireBytes + 1)));
    await expect(media.load(ref, new AbortController().signal)).rejects.toThrow(
      "response is too large",
    );
    fetch.mockResolvedValueOnce(Response.json(peer.seal(frame("media.get", {}, "wrong-ref"))));
    await expect(media.load(ref, new AbortController().signal)).rejects.toThrow(
      "Invalid attachment response",
    );
    fetch.mockResolvedValueOnce(Response.json(peer.seal(frame("media.get", {}, "wrong-ref"))));
    await expect(media.upload([new File(["x"], "file")])).rejects.toThrow(
      "Invalid attachment response",
    );
    expect(createURL).not.toHaveBeenCalled();
  });

  it("rejects malformed decrypted bundles and invalid refs without object URLs", async () => {
    const { peer, fetch, media } = await setup();
    await expect(media.load("https://evil.example", new AbortController().signal)).rejects.toThrow(
      "Invalid attachment reference",
    );
    const prefix = Buffer.alloc(4);
    prefix.writeUInt32BE(MaxMediaHeaderBytes + 1);
    const files: MediaFile[] = [{ filename: "file", content_type: "text/plain", size: 1 }];
    for (const bytes of [
      Buffer.alloc(1),
      prefix,
      bundle(files, new Uint8Array(2)),
      bundle([...files, ...files], new Uint8Array(2)),
    ]) {
      fetch.mockResolvedValueOnce(
        Response.json(peer.seal(frame("media.get", {}, ref), mediaType, bytes)),
      );
      await expect(media.load(ref, new AbortController().signal)).rejects.toThrow(
        /Invalid attachment/u,
      );
    }
    fetch.mockResolvedValueOnce(
      Response.json(peer.seal(frame("media.get", {}, ref), "text/plain", Buffer.from("bad"))),
    );
    await expect(media.load(ref, new AbortController().signal)).rejects.toThrow(
      "Invalid attachment content",
    );
    expect(createURL).not.toHaveBeenCalled();
  });

  it("limits provisional decrypted bytes before parsing metadata", async () => {
    const { content, peer, fetch, media } = await setup();
    fetch.mockResolvedValue(
      Response.json(peer.seal(frame("media.get", {}, ref), mediaType, Buffer.from("ignored"))),
    );
    vi.spyOn(content, "openStream").mockImplementation(async (_frame, _stream, _chunks, write) => {
      await write(new Uint8Array(MaxMediaPlainBytes + 1));
      return mediaType;
    });
    await expect(media.load(ref, new AbortController().signal)).rejects.toThrow(
      "response is too large",
    );
  });
});
