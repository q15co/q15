import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { Transport } from "../application/ports";
import type { ChatState } from "../domain/chat";

import { ChatStore } from "../application/chat-store";
import { parsePage } from "../domain/protocol";
import parts from "../fixtures/protocol/parts.json";
import { required } from "../testing/required";
import { Composer } from "./composer";
import { MessageView } from "./message";
import { PartView } from "./parts";
afterEach(cleanup);

function messageInput() {
  const input = screen.getByLabelText("Message q15");
  if (!(input instanceof HTMLTextAreaElement)) throw new Error("Expected a message textarea.");
  return input;
}

describe("message rendering", () => {
  it("renders every frozen part including visible media and unknown-type fallbacks", () => {
    const message = required(required(parsePage(parts).turns[0]).messages[0]);
    const { container } = render(
      <MessageView message={{ ...message, key: "42:1", turn: "42", ts: "2026-10-01T00:00:00Z" }} />,
    );
    expect(screen.getByText("Thinking")).toBeDefined();
    expect(screen.getByText("bash")).toBeDefined();
    expect(screen.getByText("Tool error")).toBeDefined();
    expect(screen.getByText("media://sha256/hash")).toBeDefined();
    expect(screen.getByText("answer")).toBeDefined();
    expect(container.querySelectorAll("details")).toHaveLength(3);
    render(
      <PartView part={{ ordinal: 9, part_type: "future_part", content: "do not discard me" }} />,
    );
    expect(screen.getByText("Unsupported part: future_part")).toBeDefined();
    expect(screen.getByText(/do not discard me/u)).toBeDefined();
  });
  it("renders reasoning and Markdown text from protocol parts", () => {
    render(
      <>
        <PartView
          part={{ ordinal: 0, part_type: "reasoning", text: "Let me think this through." }}
        />
        <PartView part={{ ordinal: 1, part_type: "text", text: "A **clear** answer." }} />
      </>,
    );
    expect(screen.getByText("Thinking")).toBeDefined();
    expect(screen.getByText("clear").tagName).toBe("STRONG");
  });
  it("renders markdown without executing embedded HTML or dangerous links", () => {
    const { container } = render(
      <PartView
        part={{
          ordinal: 0,
          part_type: "text",
          text: "<script>alert(1)</script>\n\n[bad](javascript:alert(1))",
        }}
      />,
    );
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector('a[href^="javascript:"]')).toBeNull();
  });
});

describe("composer", () => {
  it("sends a second message while active and Stop targets the active turn", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    const transport: Transport = {
      start: vi.fn<Transport["start"]>(),
      stop: vi.fn<Transport["stop"]>(),
      send: vi.fn<Transport["send"]>(),
      abort: vi.fn<Transport["abort"]>(),
      sync: vi.fn<Transport["sync"]>(),
      presence: vi.fn<Transport["presence"]>(),
    };
    const store = new ChatStore(transport, () =>
      Promise.resolve({
        turns: [],
        head_seq: "42",
        has_more: false,
      }),
    );
    const state = {
      ...store.getSnapshot(),
      connection: "connected",
      active: "42",
    } satisfies ChatState;
    const { rerender } = render(<Composer store={store} state={state} />);
    const input = messageInput();
    fireEvent.change(input, { target: { value: "next question" } });
    fireEvent.click(screen.getByLabelText("Queue message"));
    expect(transport.send).toHaveBeenCalledWith("next question", expect.any(String));
    expect(input.value).toBe("");
    // The store owns the authoritative active turn.
    store.consume({
      v: 1,
      id: "start",
      type: "turn.start",
      ts: "",
      seq: "0",
      payload: { turn: "42", msg: { turn: "42", ordinal: -1 } },
    });
    fireEvent.click(screen.getByLabelText("Stop response"));
    expect(transport.abort).toHaveBeenCalledWith("42");
    store.consume({
      v: 1,
      id: "finish",
      type: "msg.final",
      ts: "",
      seq: "0",
      payload: { msg: { turn: "42", ordinal: -1 }, full_text: "", status: "aborted" },
    });
    rerender(
      <Composer store={store} state={{ ...store.getSnapshot(), connection: "connected" }} />,
    );
    expect(screen.queryByLabelText("Stop response")).toBeNull();
    expect(screen.getByLabelText("Send message")).toBeDefined();
    store.stop();
    vi.unstubAllGlobals();
  });
  it("keeps the input when a send fails and never sends while composing text", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    const transport: Transport = {
      start: vi.fn<Transport["start"]>(),
      stop: vi.fn<Transport["stop"]>(),
      send: vi.fn<Transport["send"]>(() => {
        throw new Error("offline");
      }),
      abort: vi.fn<Transport["abort"]>(),
      sync: vi.fn<Transport["sync"]>(),
      presence: vi.fn<Transport["presence"]>(),
    };
    const store = new ChatStore(transport, () =>
      Promise.resolve(parsePage({ turns: [], head_seq: "0", has_more: false })),
    );
    render(<Composer store={store} state={{ ...store.getSnapshot(), connection: "connected" }} />);
    const input = messageInput();
    fireEvent.change(input, { target: { value: "my draft" } });
    fireEvent.keyDown(input, { key: "Enter", isComposing: true });
    expect(transport.send).not.toHaveBeenCalled();
    fireEvent.click(screen.getByLabelText("Send message"));
    expect(input.value).toBe("my draft");
    vi.unstubAllGlobals();
  });
});
