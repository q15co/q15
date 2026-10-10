import { MaxMediaFiles } from "../generated/protocol";

/** Where the service worker keeps an Android share until the app collects it. */
export const ShareCacheName = "q15-share";
/** The manifest posts a share here, and the service worker answers it without a network hop. */
export const SharePath = "/share";
/** Field name the manifest declares for the shared files. */
export const ShareField = "files";
/** Header carrying a stashed entry's original filename, percent-encoded so it stays ASCII. */
export const ShareFilenameHeader = "x-q15-filename";
/** Longest an uncollected share waits before it is dropped. */
export const ShareLifetimeMs = 24 * 60 * 60 * 1000;
/**
 * Backstop for the device's own storage. Well above the upload limit on purpose: an image is
 * downscaled before upload, so only the composer can judge whether a bundle of photographs fits.
 */
export const ShareMaxBytes = 64 * 1024 * 1024;
/** Redirect reason the service worker reports when it could not keep a share. */
export const ShareRejected = "rejected";

const entryPrefix = "/share-inbox/";

export interface ShareEntry {
  readonly receivedAt: number;
  readonly index: number;
  readonly path: string;
}

export function shareEntryPath(receivedAt: number, index: number): string {
  return `${entryPrefix}${receivedAt}-${index}`;
}

export function parseShareEntry(path: string): ShareEntry | undefined {
  if (!path.startsWith(entryPrefix)) return undefined;
  const [receivedAt, index, ...rest] = path.slice(entryPrefix.length).split("-");
  if (rest.length > 0 || receivedAt === undefined || index === undefined) return undefined;
  if (!/^\d+$/u.test(receivedAt) || !/^\d+$/u.test(index)) return undefined;
  const stamp = Number(receivedAt);
  if (!Number.isSafeInteger(stamp) || stamp <= 0) return undefined;
  return { receivedAt: stamp, index: Number(index), path };
}

export function isExpiredShare(entry: ShareEntry, now: number): boolean {
  return now - entry.receivedAt > ShareLifetimeMs;
}

/**
 * Accepts only a share the composer can send, and refuses the rest whole so no file disappears
 * without a word. The count is the composer's own limit; bytes wait for its image downscaling.
 */
export function shareFits(sizes: readonly number[]): boolean {
  return (
    sizes.length > 0 &&
    sizes.length <= MaxMediaFiles &&
    sizes.reduce((total, size) => total + size, 0) <= ShareMaxBytes
  );
}

/** Reader-facing explanation for a share the service worker could not keep. */
export function shareNotice(reason: string | null): string | undefined {
  return reason === ShareRejected
    ? "q15 could not attach that share. Pick the file in q15 instead."
    : undefined;
}
