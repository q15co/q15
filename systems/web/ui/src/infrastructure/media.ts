import type { Media, MediaObject } from "../application/ports";
import type { Frame } from "../generated/protocol";
import type { ContentSession } from "./seal";

import { inlineImages, parseMediaFiles, parseMediaResult } from "../domain/media";
import { parseSealed, parseWireFrame } from "../domain/protocol";
import {
  MaxMediaBytes,
  MaxMediaFiles,
  MaxMediaHeaderBytes,
  MaxMediaPlainBytes,
  MaxMediaWireBytes,
} from "../generated/protocol";
import { frame } from "./envelope";
import { authenticatedFetch } from "./proof";

const mediaType = "application/vnd.q15.media";

async function downscale(file: File): Promise<File> {
  if (!inlineImages.includes(file.type)) return file;
  const image = await createImageBitmap(file);
  try {
    const scale = Math.min(1, 2048 / Math.max(image.width, image.height));
    if (scale === 1) return file;
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(image.width * scale));
    canvas.height = Math.max(1, Math.round(image.height * scale));
    const context = canvas.getContext("2d");
    if (!context) throw new Error("This image could not be resized.");
    context.drawImage(image, 0, 0, canvas.width, canvas.height);
    const kind = file.type === "image/jpeg" ? "image/jpeg" : "image/png";
    const blob = await new Promise<Blob>((resolve, reject) => {
      canvas.toBlob(
        (value) => (value ? resolve(value) : reject(new Error("This image could not be resized."))),
        kind,
        0.9,
      );
    });
    const name = file.name.replace(/\.[^.]*$/u, "") + (kind === "image/jpeg" ? ".jpg" : ".png");
    return new File([blob], name, { type: kind });
  } finally {
    image.close();
  }
}

async function responseFrame(response: Response): Promise<Frame> {
  if (!response.ok)
    throw new Error(
      response.status === 413
        ? "Attachments exceed the 8 MiB limit."
        : response.status === 401
          ? "Reconnect or sign in again to load attachments."
          : "Attachment transfer failed. Try again.",
    );
  if (!response.body) throw new Error("Attachment response is empty.");
  const reader = response.body.getReader();
  const parts: Uint8Array<ArrayBuffer>[] = [];
  let size = 0;
  try {
    for (;;) {
      const next = await reader.read();
      if (next.done) break;
      size += next.value.length;
      if (size > MaxMediaWireBytes) throw new Error("Attachment response is too large.");
      parts.push(next.value);
    }
    return parseWireFrame(await new Blob(parts).text());
  } finally {
    await reader.cancel();
  }
}

export function mediaAdapter(content: ContentSession): Media {
  return {
    async upload(files) {
      if (files.length === 0 || files.length > MaxMediaFiles)
        throw new Error("Choose between 1 and 16 attachments.");
      const channel = await content.channel();
      const prepared = await Promise.all(files.map((file) => downscale(file)));
      const size = prepared.reduce((sum, file) => sum + file.size, 0);
      if (size > MaxMediaBytes) throw new Error("Attachments exceed the 8 MiB limit.");
      const header = new TextEncoder().encode(
        JSON.stringify(
          prepared.map((file) => ({
            filename: file.name,
            content_type: file.type,
            size: file.size,
          })),
        ),
      );
      if (header.length > MaxMediaHeaderBytes) throw new Error("Attachment names are too long.");
      const prefix = new Uint8Array(4);
      new DataView(prefix.buffer).setUint32(0, header.length);
      const data = new Uint8Array(await new Blob([prefix, header, ...prepared]).arrayBuffer());
      const request = frame("media.put", {});
      const sealed = await content.wrap(request, mediaType, data);
      if ((await content.channel()) !== channel)
        throw new Error("Chat reconnected. Try sending again.");
      const response = await authenticatedFetch("/api/media", {
        method: "POST",
        headers: { "Content-Type": "application/json", "Q15-Channel": channel },
        body: JSON.stringify(sealed),
      });
      const wire = await responseFrame(response);
      if (wire.type !== "media.done" || wire.id !== request.id)
        throw new Error("Invalid attachment response.");
      return parseMediaResult((await content.open(wire)).payload).parts;
    },
    async load(ref, signal): Promise<MediaObject> {
      if (!/^media:\/\/sha256\/[a-f\d]{64}$/u.test(ref))
        throw new Error("Invalid attachment reference.");
      const channel = await content.channel(signal);
      const response = await authenticatedFetch(
        `/api/media/${ref.slice("media://sha256/".length)}`,
        { headers: { "Q15-Channel": channel }, signal },
      );
      const wire = await responseFrame(response);
      if (wire.type !== "media.get" || wire.id !== ref)
        throw new Error("Invalid attachment response.");
      const sealed = parseSealed(wire.payload);
      const parts: Uint8Array<ArrayBuffer>[] = [];
      let size = 0;
      const kind = await content.openStream(wire, sealed.stream, sealed.chunks, (bytes) => {
        size += bytes.length;
        if (size > MaxMediaPlainBytes) throw new Error("Attachment response is too large.");
        parts.push(bytes);
        return Promise.resolve();
      });
      if (kind !== mediaType) throw new Error("Invalid attachment content.");
      const bytes = new Uint8Array(await new Blob(parts).arrayBuffer());
      if (bytes.length < 4) throw new Error("Invalid attachment header.");
      const length = new DataView(bytes.buffer).getUint32(0);
      if (length > MaxMediaHeaderBytes || length + 4 > bytes.length)
        throw new Error("Invalid attachment header.");
      const descriptors: unknown = JSON.parse(
        new TextDecoder("utf-8", { fatal: true }).decode(bytes.subarray(4, 4 + length)),
      );
      const files = parseMediaFiles(descriptors);
      const file = files[0];
      if (!file || files.length !== 1 || file.size !== bytes.length - length - 4)
        throw new Error("Invalid attachment size.");
      const url = URL.createObjectURL(
        new Blob([bytes.subarray(4 + length)], { type: file.content_type }),
      );
      return {
        url,
        filename: file.filename,
        contentType: file.content_type,
        dispose: () => URL.revokeObjectURL(url),
      };
    },
  };
}
