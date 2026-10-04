import type { Page, Part } from "../generated/protocol";

export function loadedHistory(count: number): Page {
  return {
    head_seq: "10001",
    has_more: false,
    turns: Array.from({ length: count }, (_, index) => ({
      seq: String(count - index),
      created_at: "2026-10-01T12:00:00Z",
      messages: [
        {
          ordinal: 0,
          role: "assistant",
          parts: [
            {
              ordinal: 0,
              part_type: "reasoning",
              text: `History ${index}: **consider** the options.`,
            },
            {
              ordinal: 1,
              part_type: "tool_call",
              tool_call: {
                id: `history-${index}`,
                name: "exec",
                arguments: `{"command":"history ${index}"}`,
              },
            },
            {
              ordinal: 2,
              part_type: "tool_result",
              tool_call_id: `history-${index}`,
              content: "Done.",
            },
            {
              ordinal: 3,
              part_type: "text",
              disposition: "final",
              text: `Answer ${index}: **Markdown** with a [link](https://example.org).\n\n- First\n- Second\n\n| A | B |\n| - | - |\n| 1 | 2 |`,
            },
          ] satisfies Part[],
        },
      ],
    })),
  };
}

export function growingAnswer(size: number) {
  return (
    (
      `A growing **answer** with a [reference][later].\n\n` +
      `- Outer\n  - Inner\n\n| A | B |\n| - | - |\n| one | two |\n\n` +
      `\`\`\`ts\nconst answer = 42;\n\`\`\`\n\n`
    ).repeat(size) + `[later]: https://example.org "Reference defined at the end"\n`
  );
}
