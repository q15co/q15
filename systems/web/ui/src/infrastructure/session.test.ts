import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";

import { proofHeaders, sessionKey } from "../testing/session-key";
import { logout } from "./session";

beforeEach(sessionKey);

afterEach(() => vi.unstubAllGlobals());

it("logs out with same-origin cookies and returns to the sign-in page", async () => {
  const replace = vi.fn<(url: string) => void>();
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("location", { replace, origin: "https://chat.example" });
  vi.stubGlobal("fetch", fetch);
  await logout();
  expect(fetch).toHaveBeenCalledWith("/auth/logout", {
    method: "POST",
    credentials: "same-origin",
    cache: "no-store",
    headers: proofHeaders,
  });
  expect(replace).toHaveBeenCalledWith("/");
  await sessionKey();
  fetch.mockResolvedValue(new Response(null, { status: 401 }));
  await logout();
  expect(replace).toHaveBeenCalledTimes(2);
  await sessionKey();
  fetch.mockResolvedValue(new Response(null, { status: 503 }));
  await expect(logout()).rejects.toThrow("Sign-out failed.");
  expect(replace).toHaveBeenCalledTimes(2);
});
