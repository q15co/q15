import {
  isExpiredShare,
  parseShareEntry,
  ShareCacheName,
  ShareFilenameHeader,
} from "../shared/share-inbox";

/** The slice of Cache Storage this adapter needs, so a test can supply a plain object. */
export interface ShareInbox {
  readonly keys: () => Promise<readonly Request[]>;
  readonly match: (request: Request) => Promise<Response | undefined>;
  readonly delete: (request: Request) => Promise<boolean>;
}

async function openShareCache(): Promise<ShareInbox | undefined> {
  if (typeof caches === "undefined") return undefined;
  // Never create the inbox: an empty cache would sit in the reader's storage from the first start.
  const names = await caches.keys();
  if (!names.includes(ShareCacheName)) return undefined;
  return caches.open(ShareCacheName);
}

function filename(response: Response, path: string): string {
  const header = response.headers.get(ShareFilenameHeader);
  if (header === null) return path.slice(path.lastIndexOf("/") + 1);
  try {
    const decoded = decodeURIComponent(header);
    return decoded === "" ? "shared-file" : decoded;
  } catch {
    return "shared-file";
  }
}

/**
 * Claims every file an Android share left behind and empties the inbox, so the reader's bytes are
 * never held twice. Only a signed-in start may do this.
 */
export async function takeSharedFiles(
  now = Date.now(),
  open: () => Promise<ShareInbox | undefined> = openShareCache,
): Promise<readonly File[]> {
  return drain("claim", now, open);
}

/**
 * Drops the shares nobody claimed in time, whatever the session state, so an unpublished share
 * cannot accumulate on the device while the reader is signed out.
 */
export async function dropExpiredShares(
  now = Date.now(),
  open: () => Promise<ShareInbox | undefined> = openShareCache,
): Promise<void> {
  await drain("expire", now, open);
}

async function drain(
  mode: "claim" | "expire",
  now: number,
  open: () => Promise<ShareInbox | undefined>,
): Promise<readonly File[]> {
  const inbox = await open();
  if (inbox === undefined) return [];
  const claiming = mode === "claim";
  const files: File[] = [];
  for (const request of await inbox.keys()) {
    const path = new URL(request.url).pathname;
    const entry = parseShareEntry(path);
    const usable = entry !== undefined && !isExpiredShare(entry, now);
    const held = claiming && usable ? await inbox.match(request) : undefined;
    if (claiming || !usable) await inbox.delete(request);
    if (held === undefined) continue;
    files.push(
      new File([await held.blob()], filename(held, path), {
        type: held.headers.get("content-type") ?? "",
      }),
    );
  }
  return files;
}
