import { expect, test } from "@playwright/test";
import { generateKeyPairSync } from "node:crypto";
import { readFileSync } from "node:fs";
import { request } from "node:http";
import { join } from "node:path";

import { parseClientFrame, parseWireFrame } from "../src/domain/protocol";
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
  const workerURLs: string[] = [];
  page.on("worker", (worker) => workerURLs.push(worker.url()));
  await page.addInitScript(() => {
    const NativeWorker = Worker;
    let fail = new URL(location.href).searchParams.has("failWorker");
    window.Worker = class extends NativeWorker {
      constructor(url: string | URL, workerOptions?: WorkerOptions) {
        document.documentElement.dataset.firstContentWorkerControlled ??= String(
          navigator.serviceWorker.controller !== null,
        );
        if (fail) {
          fail = false;
          throw new Error("Injected worker startup failure");
        }
        super(url, workerOptions);
      }
    };
  });
  const chatFrames: string[] = [];
  page.on("websocket", (socket) => {
    socket.on("framesent", (event) => chatFrames.push(String(event.payload)));
    socket.on("framereceived", (event) => chatFrames.push(String(event.payload)));
  });
  const response = await page.goto(origin);
  expect(response?.status()).toBe(401);
  expect(response?.headers()["content-security-policy"]).toContain("worker-src 'self' blob:");
  expect(response?.headers()["content-security-policy"]).not.toContain("unsafe-eval");
  expect(await page.locator('link[rel="icon"]').getAttribute("href")).toMatch(
    /^data:image\/svg\+xml,/u,
  );
  expect(await page.locator('link[rel="manifest"]').count()).toBe(0);
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
  await expect(page.locator('link[rel="manifest"]')).toHaveAttribute(
    "href",
    "/manifest.webmanifest",
  );
  expect(await page.evaluate(() => navigator.serviceWorker.controller !== null)).toBe(true);
  const appManifest = await cdp.send("Page.getAppManifest");
  expect(appManifest.url).toBe(origin + "/manifest.webmanifest");
  expect(appManifest.errors).toEqual([]);
  const manifest: unknown = JSON.parse(required(appManifest.data));
  expect(manifest).toMatchObject({ name: "q15 · Chat", start_url: "/" });
  await page.evaluate(async () => {
    for (const name of await caches.keys()) {
      if (name.startsWith("q15-shell-")) await (await caches.open(name)).delete("/icon.svg");
    }
    await new Promise<void>((resolve, reject) => {
      const image = document.createElement("img");
      image.addEventListener(
        "load",
        () => {
          image.remove();
          resolve();
        },
        { once: true },
      );
      image.addEventListener(
        "error",
        () => reject(new Error("Native icon cache miss failed authentication.")),
        { once: true },
      );
      image.hidden = true;
      image.src = "/icon.svg";
      document.body.append(image);
    });
  });
  const history = await historyResponse;
  const capturedBody = await history.text();
  expect(capturedBody).not.toContain("Saved sealed history");
  expect(capturedBody).not.toContain("part_type");
  await expect(page.getByText("Saved sealed history", { exact: true })).toBeVisible();
  expect(page.workers()).toHaveLength(1);
  expect(workerURLs.every((url) => url.startsWith("blob:"))).toBe(true);
  expect(await page.locator("html").getAttribute("data-first-content-worker-controlled")).toBe(
    "false",
  );
  await page.getByLabel("Message q15").fill("sealed browser send");
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await expect(page.getByText("Sealed agent reply", { exact: true })).toBeVisible();
  expect(chatFrames.join("\n")).not.toContain("sealed browser send");
  expect(chatFrames.join("\n")).not.toContain("Sealed agent reply");
  expect(chatFrames.join("\n")).not.toContain("Saved sealed history");
  const stateDirectory = required(process.env.Q15_WEB_TEST_STATE_DIR);
  const persisted = readFileSync(join(stateDirectory, "auth", "auth.json"), "utf8");
  const audit = readFileSync(join(stateDirectory, "web.log"), "utf8");
  expect(audit).toContain('"event":"login"');
  for (const marker of [
    "sealed browser send",
    "Sealed agent reply",
    "Saved sealed history",
    '"private_key"',
    '"chunks"',
  ]) {
    expect(persisted).not.toContain(marker);
    expect(audit).not.toContain(marker);
  }

  const capturedProof = required(history.request().headers()["q15-proof"]);
  for (const path of ["/", "/api/turns", "/ws", "/manifest.webmanifest", "/icon.svg"])
    expect((await context.request.get(origin + path)).status()).toBe(401);
  expect(
    (
      await context.request.get(history.url(), { headers: { "Q15-Proof": capturedProof } })
    ).status(),
  ).toBe(401);
  await page.evaluate(() => navigator.serviceWorker.ready);
  expect((await page.reload())?.status()).toBe(200);
  await expect(page.getByRole("button", { name: "Sign out", exact: true })).toBeVisible();
  await expect(page.getByText("Saved sealed history", { exact: true })).toBeVisible();
  await expect(page.getByText("sealed browser send", { exact: true })).toBeVisible();
  expect(page.workers()).toHaveLength(1);
  expect(await page.locator("html").getAttribute("data-first-content-worker-controlled")).toBe(
    "true",
  );
  const publicKeys = chatFrames.flatMap((data) => {
    if (parseWireFrame(data).type !== "hello") return [];
    const value = parseClientFrame(data);
    return value.type === "hello" ? [value.payload.public_key] : [];
  });
  expect(publicKeys.length).toBeGreaterThanOrEqual(2);
  expect(new Set(publicKeys).size).toBe(publicKeys.length);
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
  const worker = required(page.workers()[0]);
  const replacement = page.waitForEvent("worker");
  await worker.evaluate(() => {
    setTimeout(() => {
      throw new Error("Injected content worker crash");
    }, 0);
  });
  await expect(
    page.getByText("Chat worker stopped. Reconnect to try again.", { exact: true }),
  ).toBeVisible();
  await replacement;
  await expect(page.getByLabel("Message q15")).toBeEnabled();
  expect(page.workers()).toHaveLength(1);
  await page.goto(origin + "/?failWorker=1");
  await expect(
    page.getByText("Chat worker could not start. Reconnect to try again.", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Saved sealed history", { exact: true })).toBeVisible();
  expect(page.workers()).toHaveLength(1);
  expect(gestures).toBe(1);
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
  expect(page.workers()).toHaveLength(0);
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
