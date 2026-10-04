import type { Part } from "../generated/protocol";
import type { ChatMessage } from "./chat";

export interface Source {
  key: string;
  message: ChatMessage;
  part: Part;
}
export interface ToolActivityItem {
  key: string;
  kind: "tool";
  source: Source;
  results: Source[];
}
export type ActivityItem = ToolActivityItem | { key: string; kind: "message"; source: Source };

// History separates calls and results into messages; live drafts contain both.
// Present either shape without changing canonical message or part identities.
export function presentTurn(messages: readonly ChatMessage[]) {
  const sources = messages
    .filter((m) => m.role !== "user")
    .flatMap((message) =>
      message.parts.map((part, i) => ({ key: `${message.key}/${i}`, message, part })),
    );
  const lastTool = sources.findLastIndex(
    (s) => s.part.part_type === "tool_call" || s.part.part_type === "tool_result",
  );
  const lastAnswer = sources.findLast(
    (s, i) =>
      i > lastTool &&
      s.message.role === "assistant" &&
      s.part.part_type === "text" &&
      (s.part.disposition ?? "") === "",
  );
  const answerParts = new Map<string, Part[]>();
  const activity: ActivityItem[] = [];
  const calls = new Map<string, ToolActivityItem[]>();
  const occurrences = new Map<string, number>();
  sources.forEach((source, i) => {
    const { part, message } = source;
    const final =
      part.part_type === "text" &&
      message.role === "assistant" &&
      (part.disposition === "final" ||
        ((part.disposition ?? "") === "" &&
          i > lastTool &&
          message.key === lastAnswer?.message.key));
    if (final || !["text", "reasoning", "tool_call", "tool_result"].includes(part.part_type)) {
      const parts = answerParts.get(message.key) ?? [];
      parts.push(part);
      answerParts.set(message.key, parts);
    } else {
      // Match the nearest preceding unmatched call, even if a provider reuses IDs.
      const paired =
        part.part_type === "tool_result" &&
        part.tool_call_id !== undefined &&
        part.tool_call_id !== ""
          ? calls.get(part.tool_call_id)?.pop()
          : undefined;
      if (paired) {
        paired.results.push(source);
        return;
      }
      const identity = `${part.part_type}:${part.tool_call?.id ?? part.tool_call_id ?? ""}`;
      const occurrence = occurrences.get(identity) ?? 0;
      occurrences.set(identity, occurrence + 1);
      const key = `${identity}:${occurrence}`;
      const item: ActivityItem =
        part.part_type === "tool_call" || part.part_type === "tool_result"
          ? { key, kind: "tool", source, results: [] }
          : { key, kind: "message", source };
      activity.push(item);
      if (
        item.kind === "tool" &&
        part.part_type === "tool_call" &&
        part.tool_call !== undefined &&
        part.tool_call.id !== ""
      ) {
        const pending = calls.get(part.tool_call.id) ?? [];
        pending.push(item);
        calls.set(part.tool_call.id, pending);
      }
    }
  });
  return {
    activity,
    answers: messages.flatMap((message) => {
      const parts = answerParts.get(message.key);
      return parts ? [{ ...message, parts }] : [];
    }),
  };
}
