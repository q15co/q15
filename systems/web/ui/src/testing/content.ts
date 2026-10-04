import type { Frame } from "../generated/protocol";

import { PLAINTEXT_FRAME_TYPES } from "../generated/protocol";

export class PlainContent {
  get needsRefresh() {
    return false;
  }
  reset() {}
  offer(cursor: string) {
    return Promise.resolve({ cursor, public_key: "test", binding: "test" });
  }
  accept() {
    return Promise.resolve();
  }
  wrap(value: Frame) {
    return Promise.resolve(value);
  }
  open(value: Frame) {
    return Promise.resolve(value);
  }
  channel() {
    return Promise.resolve("channel");
  }
}
export const hasContent = (type: string) =>
  !PLAINTEXT_FRAME_TYPES.some((control) => control === type);
