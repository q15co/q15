import type { OwnerAuthentication } from "../application/auth";

import { isRecord } from "../shared/type-guards";
import { authenticatedFetch, createSessionKey, saveSessionKey } from "./proof";

export async function hasSession() {
  try {
    return (await authenticatedFetch("/auth/session")).status === 204;
  } catch {
    return false;
  }
}

async function post(path: string, body: unknown) {
  return fetch(path, {
    method: "POST",
    credentials: "same-origin",
    cache: "no-store",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

function publicKeyOptions(options: unknown) {
  if (!isRecord(options) || !isRecord(options.publicKey))
    throw new Error("Invalid authenticator options.");
  return options.publicKey;
}

function requestOptions(value: unknown): PublicKeyCredentialRequestOptionsJSON {
  const options = publicKeyOptions(value);
  if (
    typeof options.challenge !== "string" ||
    typeof options.rpId !== "string" ||
    options.userVerification !== "required"
  )
    throw new Error("Invalid sign-in options.");
  return {
    challenge: options.challenge,
    rpId: options.rpId,
    userVerification: "required",
    timeout: 120_000,
  };
}

function creationOptions(value: unknown): PublicKeyCredentialCreationOptionsJSON {
  const options = publicKeyOptions(value);
  const { rp, user, challenge, pubKeyCredParams } = options;
  if (
    typeof challenge !== "string" ||
    !isRecord(rp) ||
    typeof rp.id !== "string" ||
    typeof rp.name !== "string" ||
    !isRecord(user) ||
    typeof user.id !== "string" ||
    typeof user.name !== "string" ||
    typeof user.displayName !== "string" ||
    !Array.isArray(pubKeyCredParams)
  )
    throw new Error("Invalid enrollment options.");
  const parameters = pubKeyCredParams.map((parameter: unknown): PublicKeyCredentialParameters => {
    if (
      !isRecord(parameter) ||
      parameter.type !== "public-key" ||
      typeof parameter.alg !== "number" ||
      !Number.isInteger(parameter.alg)
    )
      throw new Error("Invalid credential algorithm.");
    return { type: "public-key", alg: parameter.alg };
  });
  return {
    challenge,
    rp: { id: rp.id, name: rp.name },
    user: { id: user.id, name: user.name, displayName: user.displayName },
    pubKeyCredParams: parameters,
    authenticatorSelection: {
      residentKey: "required",
      requireResidentKey: true,
      userVerification: "required",
    },
    attestation: "none",
    timeout: 300_000,
  };
}

export const ownerAuthentication: OwnerAuthentication = {
  async signIn() {
    const sessionKey = await createSessionKey();
    const begin = await post("/auth/login", { public_key: sessionKey.publicKey });
    if (
      begin.status !== 401 ||
      begin.headers.get("Content-Type")?.includes("application/json") !== true
    )
      throw new Error("Sign-in is unavailable. Try again shortly.");
    const options: unknown = await begin.json();
    const credential = await navigator.credentials.get({
      publicKey: PublicKeyCredential.parseRequestOptionsFromJSON(requestOptions(options)),
    });
    if (!(credential instanceof PublicKeyCredential)) throw new Error("Sign-in was cancelled.");
    const finish = await post("/auth/login/finish", credential.toJSON());
    if (finish.status !== 204)
      throw new Error("Sign-in was refused. Use an enrolled device and try again.");
    await saveSessionKey(sessionKey.key, finish.headers.get("Q15-Session") ?? "");
    location.replace("/");
  },
  async createCredential(options) {
    const value: unknown = JSON.parse(options);
    const credential = await navigator.credentials.create({
      publicKey: PublicKeyCredential.parseCreationOptionsFromJSON(creationOptions(value)),
    });
    if (!(credential instanceof PublicKeyCredential))
      throw new Error("Credential creation was cancelled.");
    return JSON.stringify(credential.toJSON());
  },
};
