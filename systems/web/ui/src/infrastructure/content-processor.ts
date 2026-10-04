import type { ContentRequest } from "../domain/content-rpc";

import {
  contentBytes,
  contentCancellation,
  MaxContentJobs,
  MaxQueuedContentBytes,
  parseContentRequest,
} from "../domain/content-rpc";
import { ContentEngine } from "./content-engine";

export function contentProcessor(
  post: (value: unknown) => void,
  engine: Pick<ContentEngine, "execute" | "needsRefresh"> = new ContentEngine(),
) {
  const queue: { request: ContentRequest; bytes: number }[] = [];
  let bytes = 0;
  let active: { request: ContentRequest; bytes: number; cancelled: boolean } | undefined;
  let failed = false;
  const running = () => !failed;

  function fail() {
    failed = true;
    queue.length = 0;
    bytes = 0;
    post({ fatal: "Content worker stopped. Reconnect to try again." });
  }

  async function drain() {
    if (active || failed) return;
    const next = queue.shift();
    if (!next) return;
    active = { ...next, cancelled: false };
    const current = active;
    const started = performance.now();
    try {
      const result = await engine.execute(current.request);
      if (!current.cancelled && running())
        post({
          id: current.request.id,
          generation: current.request.generation,
          refresh: engine.needsRefresh,
          elapsed: performance.now() - started,
          result,
        });
    } catch (error) {
      if (!current.cancelled && running())
        post({
          id: current.request.id,
          generation: current.request.generation,
          refresh: engine.needsRefresh,
          elapsed: performance.now() - started,
          result: {
            kind: "error",
            code: "invalid_frame",
            message: error instanceof Error ? error.message : "Invalid content frame.",
          },
        });
    } finally {
      if (running()) bytes -= current.bytes;
      active = undefined;
      void drain();
    }
  }

  return (data: unknown) => {
    if (failed) return;
    const cancellation = contentCancellation(data);
    if (cancellation) {
      if (
        active?.request.id === cancellation.id &&
        active.request.generation === cancellation.generation
      )
        active.cancelled = true;
      const index = queue.findIndex(
        ({ request }) =>
          request.id === cancellation.id && request.generation === cancellation.generation,
      );
      if (index >= 0) {
        const [removed] = queue.splice(index, 1);
        if (removed) bytes -= removed.bytes;
      }
      return;
    }
    try {
      const request = parseContentRequest(data);
      const size = contentBytes(request);
      if (
        queue.length + (active ? 1 : 0) >= MaxContentJobs ||
        bytes + size > MaxQueuedContentBytes
      ) {
        fail();
        return;
      }
      queue.push({ request, bytes: size });
      bytes += size;
      void drain();
    } catch {
      fail();
    }
  };
}
