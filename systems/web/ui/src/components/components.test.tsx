import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { Transport } from "../application/ports";
import type { ChatState, Pending } from "../domain/chat";

import { ChatStore } from "../application/chat-store";
import { parsePage } from "../domain/protocol";
import parts from "../fixtures/protocol/parts.json";
import { required } from "../testing/required";
import { Composer } from "./composer";
import { MessageView, PendingMessage } from "./message";
import { PartView } from "./parts";
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  Reflect.deleteProperty(navigator, "clipboard");
});

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
    expect(screen.getByTitle("Attachment")).toBeDefined();
    expect(screen.getByText("answer")).toBeDefined();
    expect(container.querySelectorAll("details")).toHaveLength(3);
    render(
      <PartView part={{ ordinal: 9, part_type: "future_part", content: "do not discard me" }} />,
    );
    expect(screen.getByText("Unsupported part: future_part")).toBeDefined();
    expect(screen.getByText(/do not discard me/u)).toBeDefined();
  });
  it("keeps part content and disclosure choices across surrounding live updates", () => {
    const cases = [
      ...required(required(parsePage(parts).turns[0]).messages[0]).parts,
      { ordinal: 9, part_type: "future_part", content: "unsupported content" },
      { ordinal: 10, part_type: "tool_result", content: "successful result" },
    ];
    for (const part of cases) {
      const { container, rerender, unmount } = render(<PartView part={part} streaming={false} />);
      const details = container.querySelector("details");
      if (details) details.open = true;
      const before = container.innerHTML;
      rerender(<PartView part={part} streaming={false} />);
      expect(container.innerHTML).toBe(before);
      rerender(<PartView part={{ ...part }} streaming={false} />);
      expect(container.innerHTML).toBe(before);
      rerender(
        <PartView part={{ ...part, text: "changed", content: "changed" }} streaming={false} />,
      );
      expect(details?.open ?? true).toBe(true);
      unmount();
    }
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

  it("keeps malformed tool arguments readable instead of dropping the input", () => {
    render(
      <PartView
        part={{
          ordinal: 0,
          part_type: "tool_call",
          tool_call: { id: "call", name: "bash", arguments: "unparseable input" },
        }}
      />,
    );
    expect(screen.getByText("unparseable input").tagName).toBe("PRE");
  });

  it("renders safe labels for parts whose optional content has not arrived yet", () => {
    const { container } = render(
      <>
        <PartView part={{ ordinal: 0, part_type: "text" }} />
        <PartView part={{ ordinal: 1, part_type: "reasoning" }} />
        <PartView part={{ ordinal: 2, part_type: "tool_call" }} />
        <PartView part={{ ordinal: 3, part_type: "tool_result" }} />
        <PartView part={{ ordinal: 4, part_type: "media" }} />
      </>,
    );
    for (const label of ["Thinking", "Tool call", "Tool result", "Media", "Attachment"])
      expect(screen.getByText(label)).toBeDefined();
    expect([...container.querySelectorAll("pre")].map((node) => node.textContent)).toEqual([
      "",
      "",
    ]);
    expect(screen.queryByText("Tool error")).toBeNull();
  });

  it("keeps copying available after a clipboard failure and clears its fallback after success", async () => {
    const writeText = vi
      .fn<Clipboard["writeText"]>()
      .mockRejectedValueOnce(new Error("permission denied"))
      .mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    render(
      <MessageView
        message={{
          key: "42:1",
          turn: "42",
          ts: "2026-10-01T00:00:00Z",
          ordinal: 1,
          role: "assistant",
          status: "completed",
          model: "model-a",
          parts: [
            { ordinal: 0, part_type: "text", text: "first" },
            { ordinal: 1, part_type: "text", text: " second" },
            { ordinal: 2, part_type: "reasoning", text: "private thinking" },
          ],
        }}
      />,
    );
    fireEvent.click(screen.getByLabelText("Copy response"));
    await screen.findByText("Copy unavailable. Select the text to copy.");
    fireEvent.click(screen.getByLabelText("Copy response"));
    await screen.findByText("Copied");
    expect(writeText).toHaveBeenLastCalledWith("first second");
    expect(screen.queryByText("Copy unavailable. Select the text to copy.")).toBeNull();
    expect(screen.queryByText("model-a")).toBeNull();
    expect(screen.queryByRole("link", { name: "Link to message 42:1" })).toBeNull();
  });

  it.each([
    { status: "aborted", label: "Stopped" },
    { status: "failed", label: "Failed" },
  ])("keeps an empty $status response visible", ({ status, label }) => {
    render(
      <MessageView
        message={{
          key: "42:1",
          turn: "42",
          ts: "2026-10-01T00:00:00Z",
          ordinal: 1,
          role: "assistant",
          status,
          parts: [],
        }}
      />,
    );
    expect(screen.getByText(label)).toBeDefined();
    expect(screen.queryByLabelText("Copy response")).toBeNull();
  });

  it("distinguishes uncertain delivery, queued sends and response failures from rejected sends", () => {
    const base = { id: "send", text: "keep my draft", afterTurn: "0" };
    const states = [
      { ...base, state: "uncertain" },
      { ...base, state: "queued" },
      { ...base, state: "accepted" },
      { ...base, state: "running", turn: "42" },
      { ...base, state: "finished", turn: "42" },
      { ...base, state: "stopped", turn: "42" },
      { ...base, state: "failed" },
      { ...base, state: "failed", turn: "42" },
    ] satisfies Pending[];
    const { rerender } = render(<PendingMessage pending={{ ...base, state: "sending" }} />);
    expect(screen.getByText("Sending…")).toBeDefined();
    for (const pending of states) {
      rerender(<PendingMessage pending={pending} />);
      expect(screen.getByText("keep my draft")).toBeDefined();
    }
    expect(screen.getByText("Response failed")).toBeDefined();
    rerender(<PendingMessage pending={{ ...base, state: "failed" }} />);
    expect(screen.getByText("Not accepted")).toBeDefined();
    rerender(<PendingMessage pending={{ ...base, state: "uncertain" }} />);
    expect(
      screen.getByText("Delivery uncertain · check history before sending again"),
    ).toBeDefined();
  });
});

describe("composer", () => {
  it("sends with Enter, preserves multiline/coarse-pointer entry and honors pending acceptance", async () => {
    let coarse = false;
    vi.stubGlobal("matchMedia", () => ({
      get matches() {
        return coarse;
      },
    }));
    const transport: Transport = {
      start: vi.fn<Transport["start"]>(),
      stop: vi.fn<Transport["stop"]>(),
      send: vi.fn<Transport["send"]>(),
      abort: vi.fn<Transport["abort"]>(),
      sync: vi.fn<Transport["sync"]>(),
      presence: vi.fn<Transport["presence"]>(),
    };
    const store = new ChatStore(transport, () =>
      Promise.resolve({ turns: [], head_seq: "0", has_more: false }),
    );
    const { rerender } = render(
      <Composer store={store} state={{ ...store.getSnapshot(), connection: "connected" }} />,
    );
    const input = messageInput();
    fireEvent.change(input, { target: { value: "hello" } });
    fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
    coarse = true;
    fireEvent.keyDown(input, { key: "Enter" });
    expect(transport.send).not.toHaveBeenCalled();
    coarse = false;
    fireEvent.keyDown(input, { key: "Enter" });
    expect(transport.send).toHaveBeenCalledOnce();
    expect(input.value).toBe("");
    rerender(
      <Composer store={store} state={{ ...store.getSnapshot(), connection: "connected" }} />,
    );
    await waitFor(() => expect(screen.getByLabelText("Queue message")).toBeDefined());
    expect(screen.getByText("Next message will be queued")).toBeDefined();
    store.stop();
  });
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
