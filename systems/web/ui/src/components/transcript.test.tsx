import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useSyncExternalStore } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { History, Transport } from "../application/ports";
import type { Page } from "../generated/protocol";

import { ChatStore } from "../application/chat-store";
import { parseFrame } from "../domain/protocol";
import { frame } from "../infrastructure/envelope";
import { required } from "../testing/required";
import { Transcript } from "./transcript";
import { ReducedMotion } from "./ui/motion-preference";

const observers: TestResizeObserver[] = [];
const frames: FrameRequestCallback[] = [];
class TestResizeObserver implements ResizeObserver {
  constructor(private readonly callback: ResizeObserverCallback) {
    observers.push(this);
  }
  observe = vi.fn<ResizeObserver["observe"]>();
  unobserve = vi.fn<ResizeObserver["unobserve"]>();
  disconnect = vi.fn<ResizeObserver["disconnect"]>();
  resize = () => this.callback([], this);
}

beforeEach(() => {
  observers.length = 0;
  frames.length = 0;
  window.history.replaceState(null, "", "/");
  vi.stubGlobal("ResizeObserver", TestResizeObserver);
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    frames.push(callback);
    return frames.length;
  });
  vi.stubGlobal("CSS", { escape: (value: string) => value.replaceAll(":", "\\:") });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function page(seq: string, hasMore = false): Page {
  return {
    head_seq: seq,
    has_more: hasMore,
    turns: [
      {
        seq,
        created_at: "2026-10-01T00:00:00Z",
        messages: [
          {
            ordinal: 0,
            role: "user",
            parts: [{ ordinal: 0, part_type: "text", text: `Question ${seq}` }],
          },
          {
            ordinal: 1,
            role: "assistant",
            parts: [{ ordinal: 0, part_type: "text", disposition: "final", text: `Answer ${seq}` }],
          },
        ],
      },
    ],
  };
}

function Harness({ store, reduced }: { store: ChatStore; reduced: boolean }) {
  const state = useSyncExternalStore(store.subscribe, store.getSnapshot);
  return (
    <ReducedMotion value={reduced}>
      <Transcript state={state} store={store} />
    </ReducedMotion>
  );
}

async function setup(history: History, reduced = true) {
  const transport: Transport = {
    start: (events) => events.connection("connected"),
    stop: () => {},
    send: () => {},
    abort: () => {},
    sync: () => {},
    presence: () => {},
  };
  const store = new ChatStore(transport, history);
  await act(async () => {
    store.start();
    await Promise.resolve();
  });
  const result = render(<Harness store={store} reduced={reduced} />);
  const node = screen.getByLabelText("Conversation");
  if (!(node instanceof HTMLDivElement)) throw new Error("Expected the transcript scroller.");
  let height = 1000;
  Object.defineProperties(node, {
    scrollHeight: { get: () => height },
    clientHeight: { value: 300 },
  });
  const scrollTo = vi.fn<(options?: ScrollToOptions) => void>();
  Object.defineProperty(node, "scrollTo", { configurable: true, value: scrollTo });
  return {
    ...result,
    store,
    node,
    scrollTo,
    resize: (value: number) => {
      height = value;
      act(() => required(observers[0]).resize());
    },
  };
}

function flushFrame() {
  act(() => {
    const scheduled = frames.splice(0);
    for (const callback of scheduled) callback(0);
  });
}

describe("transcript navigation", () => {
  it.each([true, false])(
    "follows new content until the reader scrolls back; reduced motion=%s",
    async (reduced) => {
      const { node, resize, scrollTo, store, unmount } = await setup(
        () => Promise.resolve(page("42")),
        reduced,
      );
      resize(1000);
      expect(node.scrollTop).toBe(1000);
      resize(1400);
      fireEvent.scroll(node);
      expect(screen.queryByText("Back to latest")).toBeNull();
      node.scrollTop = 400;
      fireEvent.scroll(node);
      expect(screen.getByText("Back to latest")).toBeDefined();
      resize(1800);
      expect(node.scrollTop).toBe(400);
      fireEvent.click(screen.getByRole("button", { name: "Back to latest" }));
      expect(scrollTo).toHaveBeenCalledWith({ top: 1800, behavior: reduced ? "auto" : "smooth" });
      node.scrollTop = 1500;
      fireEvent.scroll(node);
      await waitFor(() => expect(screen.queryByText("Back to latest")).toBeNull());
      act(() => {
        store.send("First pending message");
        store.consume(
          parseFrame(
            JSON.stringify(frame("turn.start", { turn: "43", msg: { turn: "43", ordinal: 0 } })),
          ),
        );
        store.send("Second pending message");
      });
      expect(screen.getByText("First pending message")).toBeDefined();
      expect(screen.getByText("Second pending message")).toBeDefined();
      unmount();
      expect(required(observers[0]).disconnect).toHaveBeenCalledOnce();
      store.stop();
    },
  );

  it("keeps the visible message anchored when an older page is prepended", async () => {
    let complete: ((value: Page) => void) | undefined;
    const older = new Promise<Page>((resolve) => {
      complete = resolve;
    });
    const history = vi
      .fn<History>()
      .mockResolvedValueOnce(page("42", true))
      .mockReturnValueOnce(older);
    const { node, resize, store } = await setup(history);
    resize(1000);
    const anchor = required(node.querySelector<HTMLElement>('[data-message-key="42:0"]'));
    let top = 120;
    vi.spyOn(anchor, "getBoundingClientRect").mockImplementation(
      () => new DOMRect(0, top, 100, 100),
    );
    vi.spyOn(anchor, "getClientRects").mockReturnValue({
      0: new DOMRect(0, 120, 100, 100),
      length: 1,
      item: (index) => (index === 0 ? new DOMRect(0, 120, 100, 100) : null),
      [Symbol.iterator]: () => [new DOMRect(0, 120, 100, 100)].values(),
    });
    node.scrollTop = 50;
    fireEvent.scroll(node);
    expect(
      screen.getByRole("button", { name: "Loading earlier messages…" }).hasAttribute("disabled"),
    ).toBe(true);
    fireEvent.scroll(node);
    expect(history).toHaveBeenCalledTimes(2);
    resize(1500);
    expect(node.scrollTop).toBe(50);
    top = 620;
    await act(async () => {
      required(complete)(page("41"));
      await older;
    });
    expect(screen.getByText("Question 41")).toBeDefined();
    expect(node.scrollTop).toBe(550);
    expect(screen.queryByRole("button", { name: "Earlier messages" })).toBeNull();
    expect(history).toHaveBeenLastCalledWith("42", expect.any(AbortSignal));
    store.stop();
  });

  it("preserves the scroll offset if a refreshed page replaces the anchor", async () => {
    let complete: ((value: Page) => void) | undefined;
    const older = new Promise<Page>((resolve) => {
      complete = resolve;
    });
    const { node, resize, store } = await setup(
      vi.fn<History>().mockResolvedValueOnce(page("42", true)).mockReturnValueOnce(older),
    );
    resize(1000);
    const anchor = required(node.querySelector<HTMLElement>('[data-message-key="42:0"]'));
    vi.spyOn(anchor, "getClientRects").mockReturnValue({
      0: new DOMRect(),
      length: 1,
      item: () => new DOMRect(),
      [Symbol.iterator]: () => [new DOMRect()].values(),
    });
    node.scrollTop = 300;
    fireEvent.click(screen.getByRole("button", { name: "Earlier messages" }));
    // A refreshed page can prune the visible DOM node while this request is pending.
    delete anchor.dataset.messageKey;
    resize(1600);
    await act(async () => {
      required(complete)(page("41"));
      await older;
    });
    expect(node.scrollTop).toBe(900);
    store.stop();
  });

  it("opens containing disclosures for message links and skips missing or malformed links", async () => {
    const toolPage: Page = {
      ...page("42"),
      turns: [
        {
          seq: "42",
          created_at: "2026-10-01T00:00:00Z",
          messages: [
            {
              ordinal: 0,
              role: "assistant",
              parts: [
                {
                  ordinal: 0,
                  part_type: "tool_call",
                  tool_call: { id: "call", name: "bash", arguments: '{"command":"pwd"}' },
                },
              ],
            },
            {
              ordinal: 1,
              role: "tool",
              parts: [
                {
                  ordinal: 0,
                  part_type: "tool_result",
                  tool_call_id: "call",
                  content: "/workspace",
                },
              ],
            },
          ],
        },
      ],
    };
    const { container, store, unmount } = await setup(() => Promise.resolve(toolPage));
    const target = required(container.querySelector<HTMLElement>('[id="message-42:1"]'));
    const scrollIntoView = vi.fn<Element["scrollIntoView"]>();
    target.scrollIntoView = scrollIntoView;
    const disclosures = [...container.querySelectorAll("details")];
    expect(disclosures).toHaveLength(2);
    expect(disclosures.every((details) => !details.open)).toBe(true);
    window.history.replaceState(null, "", "#message-42:1");
    await act(async () => {
      fireEvent(window, new HashChangeEvent("hashchange"));
      await Promise.resolve();
    });
    flushFrame();
    expect(scrollIntoView).toHaveBeenCalledWith({ block: "center" });
    expect(disclosures.every((details) => details.open && details.dataset.instant === "")).toBe(
      true,
    );
    flushFrame();
    flushFrame();
    expect(disclosures.every((details) => details.dataset.instant === undefined)).toBe(true);
    expect(container.querySelector("pre")?.textContent).toBe('{\n  "command": "pwd"\n}');
    for (const hash of ["#message-999:0", "#message-invalid", "#other"]) {
      window.history.replaceState(null, "", hash);
      await act(async () => {
        fireEvent(window, new HashChangeEvent("hashchange"));
        await Promise.resolve();
      });
      flushFrame();
    }
    expect(scrollIntoView).toHaveBeenCalledOnce();
    unmount();
    fireEvent(window, new HashChangeEvent("hashchange"));
    expect(frames).toHaveLength(0);
    store.stop();
  });

  it("loads history for a deep link while keeping duplicate pagination requests disabled", async () => {
    let complete: ((value: Page) => void) | undefined;
    const older = new Promise<Page>((resolve) => {
      complete = resolve;
    });
    const history = vi
      .fn<History>()
      .mockResolvedValueOnce(page("42", true))
      .mockReturnValueOnce(older);
    const { store, container } = await setup(history);
    window.history.replaceState(null, "", "#message-41:1");
    await act(async () => {
      fireEvent(window, new HashChangeEvent("hashchange"));
      await Promise.resolve();
    });
    expect(screen.getByText("Finding your message…")).toBeDefined();
    fireEvent.click(screen.getByRole("button", { name: "Loading earlier messages…" }));
    expect(history).toHaveBeenCalledTimes(2);
    await act(async () => {
      required(complete)(page("41"));
      await older;
    });
    expect(screen.queryByText("Finding your message…")).toBeNull();
    const target = required(container.querySelector<HTMLElement>('[id="message-41:1"]'));
    const scrollIntoView = vi.fn<Element["scrollIntoView"]>();
    target.scrollIntoView = scrollIntoView;
    flushFrame();
    expect(scrollIntoView).toHaveBeenCalledWith({ block: "center" });
    flushFrame();
    flushFrame();
    store.stop();
  });
});
