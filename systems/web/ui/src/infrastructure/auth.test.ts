import { createHash } from "node:crypto";
import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";

import { isRecord } from "../shared/type-guards";
import { required } from "../testing/required";
import { sessionKey } from "../testing/session-key";
import { hasSession, ownerAuthentication } from "./auth";
import { createSessionKey } from "./proof";

const creation = {
  publicKey: {
    challenge: "challenge",
    rp: { id: "chat.example", name: "q15" },
    user: { id: "owner", name: "owner", displayName: "q15 owner" },
    pubKeyCredParams: [{ type: "public-key", alg: -7 }],
  },
};
const assertion = {
  publicKey: { challenge: "challenge", rpId: "chat.example", userVerification: "required" },
};
const get = vi.fn<() => Promise<Credential | null>>();
const create = vi.fn<() => Promise<Credential | null>>();
const fetch = vi.fn<typeof globalThis.fetch>();
const replace = vi.fn<(url: string) => void>();

class DeviceCredential implements Credential {
  readonly id = "public-id";
  readonly type = "public-key";
  static parseRequestOptionsFromJSON() {
    return { challenge: new ArrayBuffer(32) };
  }
  static parseCreationOptionsFromJSON() {
    return {
      challenge: new ArrayBuffer(32),
      rp: { name: "q15" },
      user: { id: new ArrayBuffer(32), name: "owner", displayName: "q15 owner" },
      pubKeyCredParams: [],
    };
  }
  toJSON() {
    return { id: "public-id", type: "public-key" };
  }
}

function submittedSessionKey() {
  const body = required(fetch.mock.calls.at(-1)?.[1]?.body);
  if (typeof body !== "string") throw new Error("Missing login body.");
  const input: unknown = JSON.parse(body);
  if (!isRecord(input) || typeof input.public_key !== "string")
    throw new Error("Missing session public key.");
  return input.public_key;
}

function challengeOptions(publicKey: string) {
  const nonce = Buffer.alloc(32, 7);
  const commitment = createHash("sha256")
    .update("q15-session-key-v1\n")
    .update(nonce)
    .update(Buffer.from(publicKey, "base64url"))
    .digest("base64url");
  return {
    ...assertion,
    publicKey: { ...assertion.publicKey, challenge: commitment },
    session_key_nonce: nonce.toString("base64url"),
  };
}

function challengeResponse(publicKey = submittedSessionKey()) {
  return Promise.resolve(Response.json(challengeOptions(publicKey), { status: 401 }));
}

beforeEach(async () => {
  await sessionKey();
  vi.stubGlobal("PublicKeyCredential", DeviceCredential);
  Object.defineProperty(navigator, "credentials", { configurable: true, value: { get, create } });
  vi.stubGlobal("location", { replace, origin: "https://chat.example" });
  vi.stubGlobal("fetch", fetch);
  fetch.mockReset();
  get.mockReset();
  create.mockReset();
  replace.mockReset();
  get.mockResolvedValue(new DeviceCredential());
  create.mockResolvedValue(new DeviceCredential());
});
afterEach(() => {
  Reflect.deleteProperty(navigator, "credentials");
  vi.unstubAllGlobals();
});

it("signs in using one-time proof, with cookie credentials handled only by the browser", async () => {
  fetch
    .mockImplementationOnce(() => challengeResponse())
    .mockResolvedValueOnce(
      new Response(null, { status: 204, headers: { "Q15-Session": "a".repeat(43) } }),
    );
  await ownerAuthentication.signIn();
  expect(fetch.mock.calls.map((call) => call[0])).toEqual(["/auth/login", "/auth/login/finish"]);
  expect(fetch.mock.calls[1]?.[1]).toMatchObject({
    credentials: "same-origin",
    cache: "no-store",
    body: JSON.stringify({ id: "public-id", type: "public-key" }),
  });
  expect(replace).toHaveBeenCalledWith("/");
});

it("refuses a challenge committed to a substituted key before the passkey gesture", async () => {
  const substituted = await createSessionKey();
  fetch.mockImplementationOnce(() => challengeResponse(substituted.publicKey));
  await expect(ownerAuthentication.signIn()).rejects.toThrow("commitment mismatch");
  expect(get).not.toHaveBeenCalled();
  expect(fetch).toHaveBeenCalledOnce();
  expect(replace).not.toHaveBeenCalled();
});

it("refuses missing, malformed and noncanonical commitment nonces before the passkey gesture", async () => {
  for (const nonce of [
    undefined,
    null,
    32,
    "",
    "!",
    "a".repeat(42),
    "a".repeat(44),
    "_".repeat(43),
  ]) {
    fetch.mockImplementationOnce(() =>
      Promise.resolve(
        Response.json(
          { ...challengeOptions(submittedSessionKey()), session_key_nonce: nonce },
          { status: 401 },
        ),
      ),
    );
    await expect(ownerAuthentication.signIn()).rejects.toThrow("Invalid session key commitment");
  }
  expect(get).not.toHaveBeenCalled();
  expect(fetch.mock.calls.every((call) => call[0] === "/auth/login")).toBe(true);
  expect(replace).not.toHaveBeenCalled();
});

it("creates enrollment attestation without making a network request", async () => {
  expect(await ownerAuthentication.createCredential(JSON.stringify(creation))).toBe(
    JSON.stringify({ id: "public-id", type: "public-key" }),
  );
  expect(create).toHaveBeenCalledOnce();
  expect(fetch).not.toHaveBeenCalled();
  create.mockResolvedValue(null);
  await expect(ownerAuthentication.createCredential(JSON.stringify(creation))).rejects.toThrow(
    "cancelled",
  );
});

it("surfaces unavailable, cancelled and refused sign-in without redirecting", async () => {
  for (const response of [
    new Response(null, { status: 503 }),
    new Response("unauthorized", { status: 401 }),
  ]) {
    fetch.mockResolvedValueOnce(response);
    await expect(ownerAuthentication.signIn()).rejects.toThrow("unavailable");
  }
  get.mockResolvedValueOnce(null);
  fetch.mockImplementationOnce(() => challengeResponse());
  await expect(ownerAuthentication.signIn()).rejects.toThrow("cancelled");
  fetch
    .mockImplementationOnce(() => challengeResponse())
    .mockResolvedValueOnce(new Response(null, { status: 401 }));
  await expect(ownerAuthentication.signIn()).rejects.toThrow("refused");
  expect(replace).not.toHaveBeenCalled();
});

it("rejects malformed sign-in options before invoking the authenticator", async () => {
  for (const value of [
    null,
    { publicKey: null },
    { publicKey: {} },
    { publicKey: { challenge: "x" } },
    { publicKey: { challenge: "x", rpId: "chat.example" } },
  ]) {
    fetch.mockResolvedValueOnce(Response.json(value, { status: 401 }));
    await expect(ownerAuthentication.signIn()).rejects.toThrow("options");
  }
  expect(get).not.toHaveBeenCalled();
});

it("validates enrollment identity and algorithms before invoking the authenticator", async () => {
  const options = creation.publicKey;
  const invalid = [
    null,
    { publicKey: {} },
    ...["challenge", "rp", "user", "pubKeyCredParams"].map((key) => ({
      publicKey: { ...options, [key]: null },
    })),
    ...["id", "name"].map((key) => ({
      publicKey: { ...options, rp: { ...options.rp, [key]: null } },
    })),
    ...["id", "name", "displayName"].map((key) => ({
      publicKey: { ...options, user: { ...options.user, [key]: null } },
    })),
    ...[
      null,
      { type: "password", alg: -7 },
      { type: "public-key", alg: "-7" },
      { type: "public-key", alg: 1.5 },
    ].map((parameter) => ({ publicKey: { ...options, pubKeyCredParams: [parameter] } })),
  ];
  for (const value of invalid)
    await expect(ownerAuthentication.createCredential(JSON.stringify(value))).rejects.toThrow(
      /Invalid/u,
    );
  expect(create).not.toHaveBeenCalled();
});

it("checks sessions without exposing the HttpOnly cookie", async () => {
  fetch.mockResolvedValueOnce(
    new Response(null, { status: 204, headers: { "Q15-Session": "a".repeat(43) } }),
  );
  expect(await hasSession()).toBe(true);
  fetch.mockResolvedValueOnce(new Response(null, { status: 401 }));
  expect(await hasSession()).toBe(false);
  fetch.mockRejectedValueOnce(new Error("offline"));
  expect(await hasSession()).toBe(false);
});
