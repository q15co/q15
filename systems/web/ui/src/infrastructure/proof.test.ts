import { IDBDatabase, IDBOpenDBRequest, IDBTransaction } from "fake-indexeddb";
import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";

import type * as Proof from "./proof";

import { required } from "../testing/required";
import { sessionKey } from "../testing/session-key";
import {
  authenticatedFetch,
  clearSessionKey,
  createSessionKey,
  requestProof,
  saveSessionKey,
} from "./proof";

beforeEach(sessionKey);
afterEach(() => vi.unstubAllGlobals());

it("stores an unexportable private key across adapter reloads and signs fresh bound requests", async () => {
  const pair = await createSessionKey();
  expect(pair.key.extractable).toBe(false);
  await expect(crypto.subtle.exportKey("pkcs8", pair.key)).rejects.toThrow(/./u);
  await expect(crypto.subtle.exportKey("jwk", pair.key)).rejects.toThrow(/./u);
  await expect(crypto.subtle.wrapKey("pkcs8", pair.key, pair.key, "AES-GCM")).rejects.toThrow(/./u);
  await saveSessionKey(pair.key, "b".repeat(43));
  vi.resetModules();
  const reloaded = await vi.importActual<typeof Proof>("./proof");
  const proof = await reloaded.requestProof("GET", "/api/turns?limit=50", "https://chat.example");
  const [stamp, nonce, encoded] = proof.split(".");
  const signature = Uint8Array.from(
    atob(required(encoded).replaceAll("-", "+").replaceAll("_", "/")),
    (value) => value.codePointAt(0) ?? 0,
  );
  const publicKey = await crypto.subtle.importKey(
    "spki",
    Uint8Array.from(
      atob(pair.publicKey.replaceAll("-", "+").replaceAll("_", "/")),
      (value) => value.codePointAt(0) ?? 0,
    ),
    { name: "ECDSA", namedCurve: "P-256" },
    true,
    ["verify"],
  );
  const message = [
    "q15-proof-v1",
    "https://chat.example",
    "b".repeat(43),
    "http",
    "GET",
    "/api/turns?limit=50",
    stamp,
    nonce,
  ].join("\n");
  expect(
    await crypto.subtle.verify(
      { name: "ECDSA", hash: "SHA-256" },
      publicKey,
      signature,
      new TextEncoder().encode(message),
    ),
  ).toBe(true);
  expect(
    await crypto.subtle.verify(
      { name: "ECDSA", hash: "SHA-256" },
      publicKey,
      signature,
      new TextEncoder().encode(message.replace("GET", "POST")),
    ),
  ).toBe(false);
  expect(await requestProof("GET", "/api/turns?limit=50", "https://chat.example")).not.toBe(proof);
  expect(await requestProof("GET", "/ws", "https://chat.example", "ws")).not.toBe(proof);
  await clearSessionKey();
  await expect(requestProof("GET", "/", "https://chat.example")).rejects.toThrow("Sign in again");
});

it("adds proof while retaining supplied headers and cancellation", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  const signal = AbortSignal.abort();
  await authenticatedFetch("/auth/logout", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    signal,
  });
  const headers = required(fetch.mock.calls[0]?.[1]?.headers);
  expect(headers).toBeInstanceOf(Headers);
  expect(new Headers(headers).get("Content-Type")).toBe("application/json");
  expect(new Headers(headers).get("Q15-Proof")).toMatch(/^\d+\.[\w-]{22}\.[\w-]{86}$/u);
  expect(fetch.mock.calls[0]?.[1]?.signal).toBe(signal);
});

it("refuses exportable keys, public keys and invalid session bindings", async () => {
  const pair = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, [
    "sign",
    "verify",
  ]);
  await expect(saveSessionKey(pair.privateKey, "a".repeat(43))).rejects.toThrow("Invalid");
  await expect(saveSessionKey(pair.publicKey, "a".repeat(43))).rejects.toThrow("Invalid");
  const safe = await createSessionKey();
  await expect(saveSessionKey(safe.key, "invalid")).rejects.toThrow("Invalid");
});

it.each(["error", "blocked"])("fails closed when IndexedDB reports %s", async (type) => {
  vi.spyOn(indexedDB, "open").mockImplementation(() => {
    const request = new IDBOpenDBRequest();
    vi.spyOn(request, "addEventListener").mockImplementation((name, listener) => {
      if (name === type && typeof listener === "function")
        queueMicrotask(() => {
          listener(new Event(type));
        });
    });
    return request;
  });
  await expect(requestProof("GET", "/", "https://chat.example")).rejects.toThrow(
    type === "error" ? "unavailable" : "blocked",
  );
});

it.each(["abort", "error"])("fails closed on transaction %s", async (type) => {
  const transaction: unknown = Reflect.get(IDBDatabase.prototype, "transaction");
  if (typeof transaction !== "function") throw new Error("Missing IndexedDB transaction");
  vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
    this: IDBDatabase,
    ...args
  ) {
    const current: unknown = Reflect.apply(transaction, this, args);
    if (!(current instanceof IDBTransaction)) throw new Error("Invalid transaction");
    vi.spyOn(current, "addEventListener").mockImplementation((name, listener) => {
      if (name === type && typeof listener === "function")
        queueMicrotask(() => {
          listener(new Event(type));
        });
    });
    return current;
  });
  await expect(clearSessionKey()).rejects.toThrow("storage failed");
});
