import type { Page } from "../generated/protocol";

import { parsePage } from "../domain/protocol";

export async function fetchHistory(before: string, signal?: AbortSignal): Promise<Page> {
  const response = await fetch(`/api/turns?after_seq=${before}&limit=50`, {
    cache: "no-store",
    credentials: "same-origin",
    ...(signal === undefined ? {} : { signal }),
  });
  if (!response.ok)
    throw new Error(
      response.status === 401
        ? "Sign in again to load your history."
        : "History could not be loaded. Try again.",
    );
  return parsePage(await response.json());
}
