import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vite-plus/test";

import type { ChatMessage } from "../domain/chat";
import type { Part } from "../generated/protocol";

import { presentTurn } from "../domain/activity";
import { parsePage } from "../domain/protocol";
import parts from "../fixtures/protocol/parts.json";
import { required } from "../testing/required";
import { TurnView } from "./activity";

afterEach(cleanup);
function message(ordinal: number, role: string, entries: Omit<Part, "ordinal">[]): ChatMessage {
  return {
    key: `42:${ordinal}`,
    turn: "42",
    ordinal,
    role,
    ts: "2026-10-01T12:00:00Z",
    parts: entries.map((part, i) => ({ ...part, ordinal: i })),
  };
}
const call = (id: string): Omit<Part, "ordinal"> => ({
  part_type: "tool_call",
  tool_call: { id, name: "exec", arguments: '{"command":"pwd"}' },
});
const result = (id: string, content: string): Omit<Part, "ordinal"> => ({
  part_type: "tool_result",
  tool_call_id: id,
  content,
});
const history = [
  message(0, "user", [{ part_type: "text", text: "Check the workspace" }]),
  message(1, "assistant", [
    { part_type: "text", text: "I’ll check it.", disposition: "commentary" },
    call("a"),
  ]),
  message(2, "tool", [result("a", "/workspace")]),
  message(3, "assistant", [
    { part_type: "text", disposition: "final", text: "The workspace is ready." },
  ]),
];

describe("turn activity", () => {
  it("pairs separate call/result messages and gives only the answer response actions", () => {
    const { container } = render(<TurnView messages={history} working={false} />);
    expect(container.querySelectorAll("article")).toHaveLength(2);
    expect(screen.getAllByLabelText("Copy response")).toHaveLength(1);
    expect(screen.getByText("Used 1 tool")).toBeDefined();
    expect(screen.getByText("The workspace is ready.")).toBeDefined();
    expect(container.querySelector("[data-agent-activity]")?.hasAttribute("open")).toBe(false);
    expect(container.querySelectorAll("[data-tool-call-id]")).toHaveLength(1);
    expect(screen.getByText("/workspace")).toBeDefined();
    for (const m of history)
      expect(container.querySelectorAll(`[id="message-${m.key}"]`)).toHaveLength(1);
  });
  it("handles the frozen mixed live message without repeating its canonical anchor", () => {
    const original = required(required(parsePage(parts).turns[0]).messages[0]);
    const { container } = render(
      <TurnView
        messages={[{ ...original, key: "42:0", turn: "42", ts: "2026-10-01T12:00:00Z" }]}
        working={false}
      />,
    );
    expect(screen.getByText("answer")).toBeDefined();
    expect(screen.getByText("media://sha256/hash")).toBeDefined();
    expect(screen.getByText("1 error")).toBeDefined();
    expect(screen.getByText("/workspace")).toBeDefined();
    expect(container.querySelectorAll('[id="message-42:0"]')).toHaveLength(1);
  });
  it("keeps incomplete calls, unmatched errors, and reused call IDs distinct", () => {
    const turns = [
      message(1, "assistant", [call("repeat"), call("repeat"), call("incomplete")]),
      message(2, "tool", [
        result("repeat", "second"),
        result("repeat", "first"),
        { ...result("missing", "orphan"), is_error: true },
      ]),
    ];
    const { activity } = presentTurn(turns);
    expect(
      activity
        .filter((item) => item.kind === "tool")
        .map((item) => item.results.map((r) => r.part.content)),
    ).toEqual([["first"], ["second"], [], []]);
    render(<TurnView messages={turns} working={false} />);
    expect(screen.getByText("orphan")).toBeDefined();
    expect(screen.getByText("No result was recorded for this call.")).toBeDefined();
    expect(screen.getAllByText("Failed")).toHaveLength(1);
  });
  it("separates legacy intermediate text from the last assistant answer", () => {
    const turns = [
      message(1, "assistant", [{ part_type: "text", text: "checking" }, call("a")]),
      message(2, "tool", [result("a", "ok")]),
      message(3, "assistant", [{ part_type: "text", text: "done" }]),
    ];
    const presentation = presentTurn(turns);
    expect(presentation.answers.map((m) => m.parts[0]?.text)).toEqual(["done"]);
    expect(presentation.activity[0]?.source.part.text).toBe("checking");
  });
  it("starts work closed and preserves the reader's disclosure choice across updates", () => {
    const { container, rerender } = render(<TurnView messages={[required(history[1])]} working />);
    const activity = required(container.querySelector<HTMLDetailsElement>("[data-agent-activity]"));
    expect(activity.open).toBe(false);
    expect(screen.getByText("Using tools…")).toBeDefined();
    expect(screen.getByText("Running command")).toBeDefined();
    activity.open = true;
    rerender(<TurnView messages={history} working={false} />);
    expect(activity.open).toBe(true);
    expect(screen.getByText("Completed")).toBeDefined();
    activity.open = false;
    rerender(<TurnView messages={[required(history[1])]} working />);
    expect(activity.open).toBe(false);
  });
  it("preserves unknown parts visibly and does not invent a duration or successful result", () => {
    render(
      <TurnView
        messages={[
          ...history.slice(0, 2),
          message(4, "assistant", [{ part_type: "future_part", content: "retained" }]),
        ]}
        working={false}
      />,
    );
    expect(screen.getByText("Unsupported part: future_part")).toBeDefined();
    expect(screen.getByText(/retained/u)).toBeDefined();
    expect(screen.getByText("No result")).toBeDefined();
    expect(screen.queryByText(/Worked for/u)).toBeNull();
  });
});
