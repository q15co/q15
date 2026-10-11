import type { Attachment, MediaFile, MediaResult } from "../generated/protocol";

import { MaxMediaFiles, MaxMediaBytes } from "../generated/protocol";
import { isRecord } from "../shared/type-guards";

export const mediaTreatments = {
  image: "image",
  audio: "audio",
  video: "download",
  document: "download",
  sticker: "download",
  animation: "download",
  video_note: "download",
} as const;
export type MediaKind = keyof typeof mediaTreatments;
export function isMediaKind(value: string): value is MediaKind {
  return Object.hasOwn(mediaTreatments, value);
}
export const inlineImages = [
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
  "image/bmp",
  "image/x-icon",
];
export const inlineAudio = [
  "audio/mpeg",
  "audio/wave",
  "audio/wav",
  "audio/ogg",
  "audio/flac",
  "audio/mp4",
  "audio/aiff",
  "audio/midi",
];

export function parseMediaFiles(value: unknown): MediaFile[] {
  if (!Array.isArray(value) || value.length === 0 || value.length > MaxMediaFiles)
    throw new Error("Invalid attachment descriptors.");
  const items: unknown[] = value;
  let total = 0;
  return items.map((item) => {
    if (
      !isRecord(item) ||
      typeof item.filename !== "string" ||
      typeof item.content_type !== "string" ||
      typeof item.size !== "number" ||
      !Number.isSafeInteger(item.size) ||
      item.size < 0
    )
      throw new Error("Invalid attachment descriptors.");
    total += item.size;
    if (total > MaxMediaBytes) throw new Error("Attachments exceed the 8 MiB limit.");
    return { filename: item.filename, content_type: item.content_type, size: item.size };
  });
}

function attachment(value: unknown): value is Attachment {
  return (
    isRecord(value) &&
    value.part_type === "media" &&
    typeof value.media_kind === "string" &&
    isMediaKind(value.media_kind) &&
    typeof value.media_ref === "string" &&
    /^media:\/\/sha256\/[a-f\d]{64}$/u.test(value.media_ref) &&
    typeof value.filename === "string" &&
    typeof value.content_type === "string"
  );
}
export function parseMediaResult(value: unknown): MediaResult {
  if (
    !isRecord(value) ||
    !Array.isArray(value.parts) ||
    value.parts.length === 0 ||
    value.parts.length > MaxMediaFiles
  )
    throw new Error("Invalid attachment response.");
  const parts: unknown[] = value.parts;
  if (!parts.every((part) => attachment(part))) throw new Error("Invalid attachment response.");
  return { parts };
}
