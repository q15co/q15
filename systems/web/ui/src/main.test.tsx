import type * as ReactDOMClient from "react-dom/client";

import { act, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { SocketLike } from "./infrastructure/transport";

import { parseClientFrame } from "./domain/protocol";
import { frame } from "./infrastructure/envelope";
import { required } from "./testing/required";
import { sessionKey } from "./testing/session-key";

const roots = vi.hoisted((): { mounted: ReactDOMClient.Root[] } => ({ mounted: [] }));
vi.mock("react-dom/client", async (getOriginal) => {
  const original = await getOriginal<typeof ReactDOMClient>();
  return {
    ...original,
    createRoot: (...args: Parameters<typeof original.createRoot>) => {
      const root = original.createRoot(...args);
      roots.mounted.push(root);
      return root;
    },
  };
});

class ReadySocket implements SocketLike {
  static created: ReadySocket[] = [];
  readyState = 0;
  onopen: SocketLike["onopen"] = null;
  onclose: SocketLike["onclose"] = null;
  onmessage: SocketLike["onmessage"] = null;
  onerror: SocketLike["onerror"] = null;
  constructor(readonly address: string) {
    ReadySocket.created.push(this);
    setTimeout(() => {
      this.readyState = 1;
      this.onopen?.(new Event("open"));
    }, 20);
  }
  send(data: string) {
    if (parseClientFrame(data).type === "hello")
      queueMicrotask(() => {
        this.onmessage?.(
          new MessageEvent("message", {
            data: JSON.stringify(frame("ready", { cursor: "0", head_seq: "0", device_id: "test" })),
          }),
        );
      });
  }
  close() {
    this.readyState = 3;
    this.onclose?.(new CloseEvent("close"));
  }
}

beforeEach(async () => {
  await sessionKey();
  vi.resetModules();
  ReadySocket.created = [];
  localStorage.clear();
  window.history.replaceState(null, "", "/");
  const root = document.createElement("div");
  root.id = "root";
  document.body.append(root);
  vi.stubGlobal("matchMedia", () => ({
    matches: true,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
  vi.stubGlobal("WebSocket", ReadySocket);
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>((path) =>
      Promise.resolve(
        path === "/auth/session" || path === "/auth/worker"
          ? new Response(null, { status: 204 })
          : Response.json({ turns: [], head_seq: "0", has_more: false }),
      ),
    ),
  );
});
afterEach(() => {
  act(() => {
    for (const root of roots.mounted.splice(0)) root.unmount();
  });
  document.body.replaceChildren();
  document.head.querySelectorAll('link[rel="manifest"]').forEach((link) => link.remove());
  Reflect.deleteProperty(navigator, "serviceWorker");
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe("browser composition root", () => {
  it("renders sign-in in the same application without opening chat adapters", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>().mockResolvedValue(new Response(null, { status: 401 })),
    );
    await act(async () => {
      await vi.importActual("./main.tsx");
    });
    expect(screen.getByRole("button", { name: "Sign in" })).toBeDefined();
    expect(ReadySocket.created).toEqual([]);
    expect(document.querySelector('link[rel="manifest"]')).toBeNull();
  });

  it("starts real adapters and keeps chat usable when production worker registration fails", async () => {
    vi.stubEnv("DEV", false);
    vi.stubEnv("PROD", true);
    const register = vi
      .fn<ServiceWorkerContainer["register"]>()
      .mockRejectedValue(new Error("worker unavailable"));
    Object.defineProperty(navigator, "serviceWorker", { configurable: true, value: { register } });
    await act(async () => {
      await vi.importActual("./main.tsx");
    });
    await screen.findByText("Connected");
    expect(ReadySocket.created[0]?.address).toMatch(/^ws:\/\//u);
    expect(register).toHaveBeenCalledWith("/sw.js");
    expect(screen.getByLabelText("Message q15")).toBeDefined();
    expect(document.querySelector('link[rel="manifest"]')).toBeNull();
  });

  it.each([false, true])(
    "publishes the manifest only once the signed-in page is controlled (initially %s)",
    async (controlled) => {
      vi.stubEnv("DEV", false);
      vi.stubEnv("PROD", true);
      const events = new EventTarget();
      const worker = {
        controller: controlled ? {} : null,
        register: vi.fn<() => Promise<void>>().mockResolvedValue(),
        addEventListener: events.addEventListener.bind(events),
      };
      Object.defineProperty(navigator, "serviceWorker", { configurable: true, value: worker });
      await act(async () => {
        await vi.importActual("./main.tsx");
      });
      expect(document.querySelectorAll('link[rel="manifest"]')).toHaveLength(Number(controlled));
      if (!controlled) {
        worker.controller = {};
        events.dispatchEvent(new Event("controllerchange"));
        events.dispatchEvent(new Event("controllerchange"));
      }
      const link = required(document.querySelector('link[rel="manifest"]'));
      expect(link.getAttribute("href")).toBe("/manifest.webmanifest");
      expect(link.getAttribute("crossorigin")).toBe("use-credentials");
      expect(document.querySelectorAll('link[rel="manifest"]')).toHaveLength(1);
    },
  );

  it("loads the development preview without creating a network adapter", async () => {
    vi.stubEnv("DEV", true);
    vi.stubEnv("PROD", false);
    window.history.replaceState(null, "", "/?preview");
    await act(async () => {
      await vi.importActual("./main.tsx");
    });
    await screen.findByText("Offline preview");
    await waitFor(() => expect(screen.getByText("answer")).toBeDefined());
    expect(ReadySocket.created).toEqual([]);
    expect(fetch).not.toHaveBeenCalled();
  });

  it("fails explicitly when the embedded shell has no root element", async () => {
    required(document.querySelector("#root")).remove();
    await expect(vi.importActual("./main.tsx")).rejects.toThrow("missing its root element");
    expect(roots.mounted).toEqual([]);
  });
});
