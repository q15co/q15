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
    const agent = required(container.querySelector("[data-agent-turn]"));
    const identity = required(agent.querySelector("[data-turn-identity]"));
    const answer = required(agent.querySelector("article"));
    const activity = required(agent.querySelector("[data-agent-activity]"));
    expect([...container.children]).toEqual([container.querySelector("[data-user-turn]"), agent]);
    expect([...agent.children]).toEqual([identity, answer, activity]);
    expect(agent.querySelectorAll("[data-turn-identity]")).toHaveLength(1);
    expect(identity.textContent).toContain("q15");
    expect(identity.querySelector("svg")).not.toBeNull();
    expect(identity.querySelector("a")?.getAttribute("href")).toBe("#message-42:1");
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
  it("shows a stable agent identity before any message or reasoning arrives", () => {
    const { container, rerender } = render(<TurnView messages={[]} working />);
    const agent = required(container.querySelector("[data-agent-turn]"));
    const identity = required(agent.querySelector("[data-turn-identity]"));
    const activity = required(agent.querySelector("[data-agent-activity]"));
    expect(identity.textContent).toBe("q15");
    expect(identity.querySelector("svg")).not.toBeNull();
    expect(identity.querySelector("a")).toBeNull();
    expect([...agent.children]).toEqual([identity, activity]);
    const reasoning = {
      ...message(1, "assistant", [{ part_type: "reasoning", text: "Considering the request." }]),
      status: "streaming",
      model: "model-a",
    };
    rerender(<TurnView messages={[reasoning]} working />);
    expect(agent.querySelector("[data-turn-identity]")).toBe(identity);
    expect(identity.textContent).toContain("model-a");
    expect(identity.querySelector("a")?.getAttribute("href")).toBe("#message-42:1");
    expect(agent.querySelector("article")).toBeNull();
    expect(activity.querySelector('[id="message-42:1"]')).not.toBeNull();
    expect(screen.getByText("Considering the request.")).toBeDefined();
    rerender(
      <TurnView
        messages={[
          {
            ...reasoning,
            parts: [...reasoning.parts, { ordinal: 1, part_type: "text", text: "An answer." }],
          },
        ]}
        working
      />,
    );
    const answer = required(agent.querySelector("article"));
    expect([...agent.children]).toEqual([identity, answer, activity]);
    expect(container.querySelectorAll('[id="message-42:1"]')).toHaveLength(1);
  });
  it("keeps multiple assistant answers in one agent turn with one identity and distinct anchors", () => {
    const messages = [
      ...history,
      message(4, "assistant", [{ part_type: "text", disposition: "final", text: "More detail." }]),
    ];
    const { container } = render(<TurnView messages={messages} working={false} />);
    const agent = required(container.querySelector("[data-agent-turn]"));
    expect(container.querySelectorAll("[data-agent-turn]")).toHaveLength(1);
    expect(agent.querySelectorAll("[data-turn-identity]")).toHaveLength(1);
    expect(agent.querySelectorAll("article")).toHaveLength(2);
    expect([...agent.children].map((child) => child.tagName)).toEqual([
      "DIV",
      "ARTICLE",
      "ARTICLE",
      "DETAILS",
    ]);
    for (const m of messages)
      expect(container.querySelectorAll(`[id="message-${m.key}"]`)).toHaveLength(1);
  });
  it("renders each reader message as its own turn without inventing idle agent work", () => {
    const { container } = render(<TurnView messages={[required(history[0])]} working={false} />);
    expect(container.children).toHaveLength(1);
    expect(container.querySelectorAll("[data-user-turn]")).toHaveLength(1);
    expect(container.querySelector("[data-agent-turn]")).toBeNull();
    expect(screen.getByText("You")).toBeDefined();
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
    expect(screen.getByTitle("Attachment")).toBeDefined();
    const agent = required(container.querySelector("[data-agent-turn]"));
    expect(screen.getByTitle("Attachment").closest("article")?.parentElement).toBe(agent);
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
