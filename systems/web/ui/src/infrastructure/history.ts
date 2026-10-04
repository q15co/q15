import type { Page } from "../generated/protocol";
import type { ContentCodec } from "./content-worker";

import { authenticatedFetch } from "./proof";

export async function fetchHistory(
  before: string,
  signal: AbortSignal | undefined,
  content: ContentCodec,
): Promise<Page> {
  const channel = await content.channel(signal);
  const response = await authenticatedFetch(`/api/turns?after_seq=${before}&limit=50`, {
    cache: "no-store",
    headers: { "Q15-Channel": channel.id },
    credentials: "same-origin",
    ...(signal === undefined ? {} : { signal }),
  });
  if (!response.ok)
    throw new Error(
      response.status === 401
        ? "Sign in again to load your history."
        : response.status === 413
          ? "A history turn is too large to load."
          : "History could not be loaded. Try again.",
    );
  return content.history(await response.arrayBuffer(), channel, signal);
}
