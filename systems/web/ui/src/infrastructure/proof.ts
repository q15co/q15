import { isRecord } from "../shared/type-guards";
import { encode } from "./base64";

export interface SessionSigner {
  readonly key: CryptoKey;
  readonly binding: string;
}

function database(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open("q15-session-key", 1);
    request.addEventListener("upgradeneeded", () => {
      request.result.createObjectStore("signer");
    });
    request.addEventListener("success", () => resolve(request.result));
    request.addEventListener("error", () =>
      reject(new Error("Session key storage is unavailable.")),
    );
    request.addEventListener("blocked", () => reject(new Error("Session key storage is blocked.")));
  });
}

async function storedSigner(value?: SessionSigner | null): Promise<unknown> {
  const db = await database();
  try {
    return await new Promise<unknown>((resolve, reject) => {
      const transaction = db.transaction("signer", value === undefined ? "readonly" : "readwrite");
      const store = transaction.objectStore("signer");
      const request =
        value === undefined
          ? store.get("current")
          : value === null
            ? store.delete("current")
            : store.put(value, "current");
      let result: unknown;
      request.addEventListener("success", () => {
        result = request.result;
      });
      transaction.addEventListener("complete", () => resolve(result));
      transaction.addEventListener("abort", () => reject(new Error("Session key storage failed.")));
      transaction.addEventListener("error", () => reject(new Error("Session key storage failed.")));
    });
  } finally {
    db.close();
  }
}

export async function createSessionKey() {
  const pair = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, [
    "sign",
    "verify",
  ]);
  return {
    key: pair.privateKey,
    publicKey: encode(new Uint8Array(await crypto.subtle.exportKey("spki", pair.publicKey))),
  };
}

export async function saveSessionKey(key: CryptoKey, binding: string) {
  if (key.extractable || key.type !== "private" || !/^[\w-]{43}$/u.test(binding))
    throw new Error("Invalid session key.");
  await storedSigner({ key, binding });
}

export async function clearSessionKey() {
  await storedSigner(null);
}

export async function sessionSigner(): Promise<SessionSigner> {
  const signer = await storedSigner();
  if (
    !isRecord(signer) ||
    !(signer.key instanceof CryptoKey) ||
    signer.key.extractable ||
    signer.key.type !== "private" ||
    typeof signer.binding !== "string"
  )
    throw new Error("Sign in again to continue.");
  return { key: signer.key, binding: signer.binding };
}

export async function requestProof(
  method: string,
  path: string,
  origin: string,
  transport = "http",
) {
  const signer = await sessionSigner();
  const stamp = Math.floor(Date.now() / 1000).toString();
  const nonce = encode(crypto.getRandomValues(new Uint8Array(16)));
  const message = [
    "q15-proof-v1",
    origin,
    signer.binding,
    transport,
    method,
    path,
    stamp,
    nonce,
  ].join("\n");
  const signature = await crypto.subtle.sign(
    { name: "ECDSA", hash: "SHA-256" },
    signer.key,
    new TextEncoder().encode(message),
  );
  return `${stamp}.${nonce}.${encode(new Uint8Array(signature))}`;
}

export async function authenticatedFetch(path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers);
  headers.set("Q15-Proof", await requestProof(init.method ?? "GET", path, location.origin));
  return fetch(path, { ...init, credentials: "same-origin", cache: "no-store", headers });
}
