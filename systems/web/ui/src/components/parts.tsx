import { Brain, ChevronDown, CircleAlert, File, Terminal, Check } from "lucide-react";
import { clsx } from "clsx";
import type { Part } from "../generated/protocol";
import { MarkdownView } from "./markdown";
import styles from "./parts.module.css";

export function pretty(value: string) {
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
}
export function PartView({ part }: { part: Part }) {
  switch (part.part_type) {
    case "text":
      return <MarkdownView text={part.text ?? ""} />;
    case "reasoning":
      return (
        <details className={clsx(styles.partCard, styles.reasoning)}>
          <summary>
            <Brain size={15} />
            <span>Thinking</span>
            <ChevronDown size={14} />
          </summary>
          <div className={styles.partContent}>
            <MarkdownView text={part.text ?? ""} />
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
          <pre className={styles.partContent}>{pretty(part.tool_call?.arguments ?? "")}</pre>
        </details>
      );
    case "tool_result":
      return (
        <details className={clsx(styles.partCard, part.is_error && styles.partError)}>
          <summary>
            {part.is_error ? <CircleAlert size={15} /> : <Check size={15} />}
            <span>{part.is_error ? "Tool error" : "Tool result"}</span>
            <ChevronDown size={14} />
          </summary>
          <pre className={styles.partContent}>{part.content ?? ""}</pre>
        </details>
      );
    case "media":
      return (
        <div className={styles.mediaReference}>
          <File size={16} />
          <div>
            <strong>{part.media_kind ?? "Media"}</strong>
            <small>{part.media_ref ?? "Attachment"}</small>
            <small>Attachment preview will be available in a future update.</small>
          </div>
        </div>
      );
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
