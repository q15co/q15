import { afterEach, expect, it, vi } from "vite-plus/test";

import { logout } from "./session";

afterEach(() => vi.unstubAllGlobals());

it("logs out with same-origin cookies and returns to the sign-in page", async () => {
  const replace = vi.fn<(url: string) => void>();
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("location", { replace });
  vi.stubGlobal("fetch", fetch);
  await logout();
  expect(fetch).toHaveBeenCalledWith("/auth/logout", {
    method: "POST",
    credentials: "same-origin",
    cache: "no-store",
  });
  expect(replace).toHaveBeenCalledWith("/");
  fetch.mockResolvedValue(new Response(null, { status: 401 }));
  await logout();
  expect(replace).toHaveBeenCalledTimes(2);
  fetch.mockResolvedValue(new Response(null, { status: 503 }));
  await expect(logout()).rejects.toThrow("Sign-out failed.");
  expect(replace).toHaveBeenCalledTimes(2);
});
