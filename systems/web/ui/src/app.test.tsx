import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { renderToString } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { History, Transport, TransportEvents } from "./application/ports";

import { App } from "./app";
import { ChatStore } from "./application/chat-store";
import { MotionProvider } from "./components/ui/motion";
import { useMotionPreference } from "./components/ui/motion-preference";
import { parseFrame, parsePage } from "./domain/protocol";
import history from "./fixtures/server/history.json";
import { frame } from "./infrastructure/envelope";
import { required } from "./testing/required";

let reduced = true;
const listeners = new Set<() => void>();
beforeEach(() => {
  reduced = true;
  listeners.clear();
  localStorage.clear();
  window.history.replaceState(null, "", "/");
  vi.stubGlobal("matchMedia", (media: string) => ({
    media,
    get matches() {
      return media.includes("prefers-color-scheme") ? false : reduced;
    },
    addEventListener: (_event: string, listener: () => void) => listeners.add(listener),
    removeEventListener: (_event: string, listener: () => void) => listeners.delete(listener),
  }));
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function setup(
  historyFn: History = () => Promise.resolve({ turns: [], head_seq: "0", has_more: false }),
) {
  const events: TransportEvents[] = [];
  const transport: Transport = {
    start: vi.fn<Transport["start"]>((handlers) => {
      events.push(handlers);
      handlers.connection("connected");
    }),
    stop: vi.fn<Transport["stop"]>(),
    send: vi.fn<Transport["send"]>(),
    abort: vi.fn<Transport["abort"]>(),
    sync: vi.fn<Transport["sync"]>(),
    presence: vi.fn<Transport["presence"]>(),
  };
  const store = new ChatStore(transport, historyFn);
  return {
    store,
    transport,
    events,
    emit: (type: string, payload: unknown) => {
      act(() => store.consume(parseFrame(JSON.stringify(frame(type, payload)))));
    },
  };
}

function MotionReadout() {
  return <span>{useMotionPreference() ? "Motion reduced" : "Motion enabled"}</span>;
}

describe("app lifecycle and interaction", () => {
  it("sends welcome suggestions, toggles navigation/theme, reports presence and cleans up subscriptions", async () => {
    const { store, transport } = setup();
    const { unmount } = render(
      <MotionProvider>
        <App store={store} />
      </MotionProvider>,
    );
    await waitFor(() => expect(store.getSnapshot().loadingHistory).toBe(false));
    expect(screen.getByText("Connected")).toBeDefined();
    fireEvent.click(screen.getByRole("button", { name: "Help me think through an idea" }));
    expect(transport.send).toHaveBeenCalledWith(
      "Help me think through an idea",
      expect.any(String),
    );
    expect(screen.queryByRole("heading", { name: "Start a conversation" })).toBeNull();
    fireEvent.click(screen.getByLabelText("Open navigation"));
    fireEvent.click(required(screen.getAllByLabelText("Close navigation")[0]));
    fireEvent.click(screen.getByLabelText("Open navigation"));
    fireEvent.click(screen.getByRole("link", { name: "Chat" }));
    fireEvent.click(screen.getByLabelText("Open navigation"));
    fireEvent.click(required(screen.getAllByLabelText("Close navigation").at(-1)));
    fireEvent.click(screen.getByLabelText("Switch to Latte theme"));
    expect(document.documentElement.dataset.theme).toBe("latte");
    expect(localStorage.getItem("q15-theme")).toBe("latte");
    vi.spyOn(document, "hidden", "get").mockReturnValue(true);
    fireEvent(document, new Event("visibilitychange"));
    expect(transport.presence).toHaveBeenLastCalledWith(false);
    vi.spyOn(document, "hidden", "get").mockReturnValue(false);
    fireEvent(document, new Event("visibilitychange"));
    expect(transport.presence).toHaveBeenLastCalledWith(true);
    unmount();
    expect(transport.stop).toHaveBeenCalledOnce();
    expect(listeners.size).toBe(0);
    fireEvent(document, new Event("visibilitychange"));
    expect(transport.presence).toHaveBeenCalledTimes(2);
  });

  it("renders persisted history, disconnection and dismissible notices/errors", async () => {
    const { store, events, emit } = setup(() => Promise.resolve(parsePage(history)));
    render(
      <MotionProvider>
        <App store={store} preview />
      </MotionProvider>,
    );
    await screen.findByText("answer");
    expect(screen.getByText("Offline preview")).toBeDefined();
    act(() => required(events[0]).connection("offline"));
    expect(screen.getByText("Disconnected. Refresh to reconnect.")).toBeDefined();
    act(() => required(events[0]).connection("reconnecting"));
    expect(screen.getByText("Reconnecting to q15…")).toBeDefined();
    emit("notice", { code: "status", text: "Agent is preparing" });
    expect(screen.getByText("Agent is preparing").closest('[role="status"]')).not.toBeNull();
    emit("error", { code: "bridge_unavailable", ref: "" });
    expect(screen.getByRole("alert").textContent).toContain("agent is unavailable");
    fireEvent.click(screen.getByLabelText("Dismiss notice"));
    expect(screen.queryByRole("alert")).toBeNull();
    act(() => required(events[0]).error("The connection failed"));
    expect(screen.getByRole("alert").textContent).toContain("connection failed");
  });

  it.each(["fulfilled", "rejected", "throws"])(
    "handles a %s install prompt without interrupting chat",
    async (outcome) => {
      const { store } = setup();
      render(
        <MotionProvider>
          <App store={store} />
        </MotionProvider>,
      );
      await waitFor(() => expect(store.getSnapshot().loadingHistory).toBe(false));
      fireEvent(window, new Event("beforeinstallprompt"));
      fireEvent(window, Object.assign(new Event("beforeinstallprompt"), { prompt: 42 }));
      expect(screen.queryByRole("button", { name: "Install" })).toBeNull();
      const prompt = vi.fn<() => Promise<void>>(() => {
        if (outcome === "throws") throw new Error("Installation unavailable");
        return outcome === "rejected"
          ? Promise.reject(new Error("Installation dismissed"))
          : Promise.resolve();
      });
      const install = Object.assign(new Event("beforeinstallprompt", { cancelable: true }), {
        prompt,
      });
      fireEvent(window, install);
      expect(install.defaultPrevented).toBe(true);
      fireEvent.click(screen.getByRole("button", { name: "Install" }));
      await waitFor(() => expect(screen.queryByRole("button", { name: "Install" })).toBeNull());
      expect(prompt).toHaveBeenCalledOnce();
      expect(screen.getByLabelText("Message q15")).toBeDefined();
    },
  );

  it("follows changes to reduced motion and supports the server snapshot", async () => {
    const { store } = setup();
    const { unmount } = render(
      <MotionProvider>
        <App store={store} />
      </MotionProvider>,
    );
    await waitFor(() => expect(store.getSnapshot().loadingHistory).toBe(false));
    act(() => {
      reduced = false;
      for (const listener of listeners) listener();
    });
    fireEvent.click(screen.getByLabelText("Switch to Latte theme"));
    fireEvent.click(screen.getByLabelText("Switch to Mocha theme"));
    expect(document.documentElement.dataset.theme).toBe("mocha");
    unmount();
    const html = renderToString(
      <MotionProvider>
        <MotionReadout />
      </MotionProvider>,
    );
    expect(html).toContain("Motion reduced");
  });
});
