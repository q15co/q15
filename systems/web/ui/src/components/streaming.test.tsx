import type { ReactNode } from "react";
import type * as markdownModule from "react-markdown";

import { act, cleanup, render } from "@testing-library/react";
import { useSyncExternalStore } from "react";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { Transport } from "../application/ports";
import type * as chatModule from "../domain/chat";
import type * as messageModule from "./message";

import { ChatStore } from "../application/chat-store";
import { parseFrame } from "../domain/protocol";
import { frame } from "../infrastructure/envelope";
import { required } from "../testing/required";
import { loadedHistory } from "../testing/streaming";
import { MarkdownView } from "./markdown";
import { Transcript } from "./transcript";
import { ReducedMotion } from "./ui/motion-preference";

const counts = vi.hoisted(() => ({ rows: 0, markdown: 0, grouped: 0 }));
vi.mock("react-markdown", async (original) => {
  const actual = await original<typeof markdownModule>();
  return {
    ...actual,
    default: (props: Parameters<typeof actual.default>[0]) => {
      counts.markdown++;
      return <actual.default {...props} />;
    },
  };
});
vi.mock("./message", async (original) => {
  const actual = await original<typeof messageModule>();
  return {
    ...actual,
    MessageView: (props: Parameters<typeof actual.MessageView>[0]) => {
      if (props.message.turn !== "10001") counts.rows++;
      return <actual.MessageView {...props} />;
    },
  };
});
vi.mock("../domain/chat", async (original) => {
  const actual = await original<typeof chatModule>();
  return {
    ...actual,
    groupTurns: (...args: Parameters<typeof actual.groupTurns>) => {
      counts.grouped += args[0].length;
      return actual.groupTurns(...args);
    },
  };
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

function Harness({ store }: { store: ChatStore }) {
  const state = useSyncExternalStore(store.subscribe, store.getSnapshot);
  return (
    <ReducedMotion value>
      <Transcript state={state} store={store} />
    </ReducedMotion>
  );
}

const transport: Transport = {
  start: () => {},
  stop: () => {},
  send: () => {},
  abort: () => {},
  sync: () => {},
  presence: () => {},
};
const msg = { turn: "10001", ordinal: -1 };
function feed(store: ChatStore, type: string, payload: unknown) {
  act(() => store.consume(parseFrame(JSON.stringify(frame(type, payload)))));
}

describe("streaming work", () => {
  it.each([100, 1000])(
    "keeps completed rows and grouping idle with %s historical messages",
    async (size) => {
      vi.useFakeTimers();
      const store = new ChatStore(transport, () => Promise.resolve(loadedHistory(size)));
      await store.loadHistory();
      const view = render(<Harness store={store} />);
      counts.rows = 0;
      feed(store, "turn.start", { turn: msg.turn, msg });
      feed(store, "snapshot", { msg, seq: "1", kind: "model_start", text: "" });
      const call = { id: "active", name: "exec", arguments: '{"command":"active"}' };
      feed(store, "delta", { msg, seq: "2", kind: "tool_call", text: "", call });
      feed(store, "delta", { msg, seq: "3", kind: "tool_result", text: "Done", call });
      feed(store, "delta", { msg, seq: "4", kind: "text", text: "Initial answer" });
      feed(store, "delta", { msg, seq: "5", kind: "reasoning", text: "Initial reasoning" });
      expect(counts.rows).toBe(0);
      const history = store.getSnapshot().messages;
      const unchanged = required(store.getSnapshot().live).parts.slice(0, 2);
      counts.rows = 0;
      counts.markdown = 0;
      counts.grouped = 0;
      const parse = vi.spyOn(JSON, "parse");
      let full = "Initial answer";
      for (let index = 0; index < 20; index++) {
        full += ` delta ${index}`;
        feed(store, index % 2 === 0 ? "delta" : "snapshot", {
          msg,
          seq: String(index + 6),
          kind: "text",
          text: index % 2 === 0 ? ` delta ${index}` : full,
        });
        feed(store, index % 2 === 0 ? "delta" : "snapshot", {
          msg,
          seq: String(index + 26),
          kind: "reasoning",
          text: `reasoning ${index}`,
        });
        act(() => {
          vi.advanceTimersByTime(16);
        });
      }
      expect(store.getSnapshot().messages).toBe(history);
      expect(required(store.getSnapshot().live).parts[0]).toBe(unchanged[0]);
      expect(required(store.getSnapshot().live).parts[1]).toBe(unchanged[1]);
      expect(counts.rows).toBe(0);
      expect(counts.grouped).toBe(0);
      expect(
        parse.mock.calls.filter(
          ([text]) => text === call.arguments || text.includes('"command":"history'),
        ),
      ).toHaveLength(0);
      expect(counts.markdown).toBe(20);
      const reasoning = required(
        [...view.container.querySelectorAll<HTMLDetailsElement>("[data-agent-activity]")].at(-1),
      );
      reasoning.open = true;
      feed(store, "msg.final", { msg, status: "aborted", full_text: full });
      expect(counts.rows).toBe(0);
      expect(view.container.textContent).toContain(full);
      expect(reasoning.open).toBe(true);
      store.stop();
    },
    30_000,
  );
});

function canonical(text: string): ReactNode {
  return <MarkdownView text={text} />;
}

describe("growing Markdown", () => {
  it("coalesces a large answer and flushes canonical fences, lists, tables and references on completion", () => {
    vi.useFakeTimers();
    const full =
      `- Outer\n  - Inner **bold**\n\n| A | B |\n| - | - |\n| x | y |\n\n\`\`\`ts\nconst x = 1;\n\`\`\`\n\n[linked][end]\n\n`.repeat(
        500,
      ) + "[end]: https://example.org\n";
    const view = render(<MarkdownView text="" streaming />);
    counts.markdown = 0;
    for (let offset = 100; offset < full.length; offset += 100) {
      view.rerender(<MarkdownView text={full.slice(0, offset)} streaming />);
      act(() => {
        vi.advanceTimersByTime(1);
      });
    }
    expect(counts.markdown).toBeLessThan(24);
    view.rerender(<MarkdownView text={full} />);
    const finished = view.container.innerHTML;
    expect(counts.markdown).toBeLessThan(25);
    view.unmount();
    const reference = render(canonical(full));
    expect(finished).toBe(reference.container.innerHTML);
    act(() => {
      vi.runAllTimers();
    });
    expect(reference.container.querySelectorAll("table")).toHaveLength(500);
    expect(reference.container.querySelectorAll("pre")).toHaveLength(500);
  }, 30_000);

  it("flushes a replacement snapshot on a bounded cadence and cancels unmounted work", () => {
    vi.useFakeTimers();
    const view = render(<MarkdownView text="start" streaming />);
    view.rerender(<MarkdownView text="replaced **snapshot**" streaming />);
    act(() => {
      vi.advanceTimersByTime(23);
    });
    expect(view.container.textContent).toBe("start");
    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(view.container.querySelector("strong")?.textContent).toBe("snapshot");
    view.rerender(<MarkdownView text="never render" streaming />);
    view.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
