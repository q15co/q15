import { IDBFactory } from "fake-indexeddb";
import { webcrypto } from "node:crypto";
import { expect, vi } from "vite-plus/test";

import { createSessionKey, saveSessionKey } from "../infrastructure/proof";

export async function sessionKey() {
  const indexedDB = new IDBFactory();
  vi.stubGlobal("indexedDB", indexedDB);
  vi.stubGlobal("crypto", webcrypto);
  const pair = await createSessionKey();
  vi.stubGlobal("CryptoKey", pair.key.constructor);
  await saveSessionKey(pair.key, "a".repeat(43));
  return { ...pair, indexedDB };
}

export const proofHeaders: unknown = expect.any(Headers);
