import type { Page } from "../generated/protocol";
import type { ContentSession } from "./seal";

import { parsePage, parseWireFrame } from "../domain/protocol";
import { authenticatedFetch } from "./proof";

export async function fetchHistory(
  before: string,
  signal: AbortSignal | undefined,
  content: ContentSession,
): Promise<Page> {
  const channel = await content.channel(signal);
  const limit = before === "0" ? 5 : 10;
  const response = await authenticatedFetch(`/api/turns?after_seq=${before}&limit=${limit}`, {
    cache: "no-store",
    headers: { "Q15-Channel": channel },
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
  const wire = parseWireFrame(await response.text());
  if (wire.type !== "history" || wire.id !== channel)
    throw new Error("The server returned unsupported history.");
  return parsePage((await content.open(wire)).payload);
}
