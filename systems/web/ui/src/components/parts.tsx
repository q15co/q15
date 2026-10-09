import { clsx } from "clsx";
import { Brain, ChevronDown, CircleAlert, Terminal, Check } from "lucide-react";

import type { Part } from "../generated/protocol";

import { MarkdownView } from "./markdown";
import { MediaView } from "./media";
import { ToolArguments, ToolResult } from "./toolcall";

import styles from "./parts.module.css";

export function PartView({ part, streaming: live }: { part: Part; streaming?: boolean }) {
  const streaming = live ?? false;
  switch (part.part_type) {
    case "text":
      return <MarkdownView text={part.text ?? ""} streaming={streaming} />;
    case "reasoning":
      return (
        <details className={clsx(styles.partCard, styles.reasoning)}>
          <summary>
            <Brain size={15} />
            <span>Thinking</span>
            <ChevronDown size={14} />
          </summary>
          <div className={styles.partContent}>
            <MarkdownView text={part.text ?? ""} streaming={streaming} />
          </div>
        </details>
      );
    case "tool_call":
      return (
        <details className={styles.partCard}>
          <summary>
            <Terminal size={15} />
            <span>{part.tool_call?.name ?? "Tool call"}</span>
            <ChevronDown size={14} />
          </summary>
          <ToolArguments raw={part.tool_call?.arguments ?? ""} className={styles.partDivider} />
        </details>
      );
    case "tool_result":
      return (
        <details className={clsx(styles.partCard, part.is_error === true && styles.partError)}>
          <summary>
            {part.is_error === true ? <CircleAlert size={15} /> : <Check size={15} />}
            <span>{part.is_error === true ? "Tool error" : "Tool result"}</span>
            <ChevronDown size={14} />
          </summary>
          <ToolResult content={part.content ?? ""} className={styles.partDivider} />
        </details>
      );
    case "media":
      return <MediaView part={part} />;
    default:
      return (
        <details className={clsx(styles.partCard, styles.partError)} open>
          <summary>
            <CircleAlert size={15} />
            <span>Unsupported part: {part.part_type}</span>
            <ChevronDown size={14} />
          </summary>
          <pre className={styles.partContent}>{JSON.stringify(part, null, 2)}</pre>
        </details>
      );
  }
}
