import { expect, test } from "@playwright/test";
import { generateKeyPairSync } from "node:crypto";
import { request } from "node:http";
import { join } from "node:path";

import { required } from "../src/testing/required";

function admin(path: string, body?: unknown): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const call = request(
      {
        socketPath: join(required(process.env.Q15_WEB_TEST_STATE_DIR), "auth", "admin.sock"),
        path,
        method: body === undefined ? "GET" : "POST",
      },
      (response) => {
        let data = "";
        response.setEncoding("utf8");
        response.on("data", (chunk: string) => {
          data += chunk;
        });
        response.on("end", () => {
          if (response.statusCode !== 200) {
            reject(new Error(data));
            return;
          }
          try {
            const value: unknown = JSON.parse(data);
            resolve(value);
          } catch (error) {
            reject(error instanceof Error ? error : new Error("Invalid admin response"));
          }
        });
      },
    );
    call.on("error", reject);
    call.end(body === undefined ? undefined : JSON.stringify(body));
  });
}

test("host enrollment, passkey sign-in, protected data and logout use the real gate", async ({
  page,
  context,
}) => {
  const cdp = await context.newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: {
      protocol: "ctap2",
      transport: "internal",
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  });
  let gestures = 0;
  cdp.on("WebAuthn.credentialAsserted", () => {
    gestures++;
  });
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const origin = "http://localhost:4184";
  const response = await page.goto(origin);
  expect(response?.status()).toBe(401);
  for (const path of ["/ws", "/api/turns", "/enroll/begin", "/auth/enroll", "/devices"]) {
    expect((await context.request.get(origin + path)).status()).toBe(401);
  }
  const options = await admin("/enroll/begin", { name: "Playwright device" });
  if (
    typeof options !== "object" ||
    options === null ||
    !("id" in options) ||
    !("options" in options)
  )
    throw new Error("Invalid enrollment options");
  await page.getByText("Enroll from Hermes", { exact: true }).click();
  await page.getByLabel("Options from Hermes").fill(JSON.stringify(options.options));
  await page.getByRole("button", { name: "Create device credential" }).click();
  await expect(page.getByLabel("Response to paste into Hermes")).not.toHaveValue("");
  const attestation: unknown = JSON.parse(
    await page.getByLabel("Response to paste into Hermes").inputValue(),
  );
  await admin("/enroll/finish", { id: options.id, response: attestation });
  const substitutedKey = generateKeyPairSync("ec", { namedCurve: "prime256v1" })
    .publicKey.export({ type: "spki", format: "der" })
    .toString("base64url");
  let finishes = 0;
  page.on("request", (call) => {
    if (new URL(call.url()).pathname === "/auth/login/finish") finishes++;
  });
  await page.route("**/auth/login", async (route) => {
    const rewritten = await route.fetch({
      headers: {
        ...(await route.request().allHeaders()),
        Origin: origin,
        "Sec-Fetch-Site": "same-origin",
      },
      postData: JSON.stringify({ public_key: substitutedKey }),
    });
    expect(rewritten.status()).toBe(401);
    expect(rewritten.headers()["content-type"]).toContain("application/json");
    await route.fulfill({ response: rewritten });
  });
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByText("Session key commitment mismatch. Sign-in was refused."),
  ).toBeVisible();
  expect(gestures).toBe(0);
  expect(finishes).toBe(0);
  await page.unroute("**/auth/login");
  const historyResponse = page.waitForResponse(
    (historyResult) =>
      new URL(historyResult.url()).pathname === "/api/turns" && historyResult.status() === 200,
  );
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Sign out", exact: true })).toBeVisible();
  const history = await historyResponse;
  const capturedProof = required(history.request().headers()["q15-proof"]);
  for (const path of ["/", "/api/turns", "/ws"])
    expect((await context.request.get(origin + path)).status()).toBe(401);
  expect(
    (
      await context.request.get(history.url(), { headers: { "Q15-Proof": capturedProof } })
    ).status(),
  ).toBe(401);
  await page.evaluate(() => navigator.serviceWorker.ready);
  expect((await page.reload())?.status()).toBe(200);
  await expect(page.getByRole("button", { name: "Sign out", exact: true })).toBeVisible();
  expect(gestures).toBe(1);
  expect(
    await page.evaluate(async () => {
      const key = await new Promise<unknown>((resolve, reject) => {
        const openStore = indexedDB.open("q15-session-key", 1);
        openStore.addEventListener("error", () => reject(new Error("Key store unavailable")));
        openStore.addEventListener("success", () => {
          const db = openStore.result;
          const transaction = db.transaction("signer", "readonly");
          const get = transaction.objectStore("signer").get("current");
          get.addEventListener("success", () => {
            const value: unknown = get.result;
            resolve(value);
          });
          transaction.addEventListener("complete", () => db.close());
        });
      });
      if (
        typeof key !== "object" ||
        key === null ||
        !("key" in key) ||
        !(key.key instanceof CryptoKey) ||
        key.key.extractable
      )
        return false;
      try {
        await crypto.subtle.exportKey("pkcs8", key.key);
        return false;
      } catch {
        return true;
      }
    }),
  ).toBe(true);
  const cookies = await context.cookies();
  const cookie = required(cookies.find((value) => value.name === "__Host-q15s"));
  expect(cookie).toMatchObject({ httpOnly: true, secure: true, sameSite: "Strict", path: "/" });
  expect(await page.evaluate(() => document.cookie)).not.toContain("q15s");
  expect(
    await page.evaluate(() =>
      Object.keys(localStorage).filter((key) => /auth|session|token|credential/iu.test(key)),
    ),
  ).toEqual([]);
  expect((await context.request.post(origin + "/auth/logout")).status()).toBe(401);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
  expect((await context.request.get(origin + "/api/turns")).status()).toBe(401);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Sign out", exact: true })).toBeVisible();
  if (typeof attestation !== "object" || attestation === null || !("id" in attestation))
    throw new Error("Invalid attestation");
  await admin("/revoke", { id: attestation.id });
  await page.reload();
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
  expect(gestures).toBe(2);
  expect(errors).toEqual([]);
});
