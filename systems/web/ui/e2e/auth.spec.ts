import { expect, test } from "@playwright/test";
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
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Sign out", exact: true })).toBeVisible();
  expect((await context.request.get(origin + "/api/turns")).status()).toBe(200);
  const cookies = await context.cookies();
  const cookie = required(cookies.find((value) => value.name === "__Host-q15s"));
  expect(cookie).toMatchObject({ httpOnly: true, secure: true, sameSite: "Strict", path: "/" });
  expect(await page.evaluate(() => document.cookie)).not.toContain("q15s");
  expect(
    await page.evaluate(() =>
      Object.keys(localStorage).filter((key) => /auth|session|token|credential/iu.test(key)),
    ),
  ).toEqual([]);
  expect((await context.request.post(origin + "/auth/logout")).status()).toBe(403);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
  expect((await context.request.get(origin + "/api/turns")).status()).toBe(401);
  expect(errors).toEqual([]);
});
