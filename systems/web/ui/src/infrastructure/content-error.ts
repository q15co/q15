import type { ContentResult } from "../domain/content-rpc";

type ErrorCode = Extract<ContentResult, { kind: "error" }>["code"];
export class ContentOperationError extends Error {
  constructor(
    readonly code: ErrorCode,
    message: string,
  ) {
    super(message);
  }
}
