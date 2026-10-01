import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  Brain,
  ChevronDown,
  CircleAlert,
  File,
  Terminal,
  Sparkles,
  Check,
  Copy,
} from "lucide-react";
import { useState } from "react";
import type { Part } from "../generated/protocol";
import type { ChatMessage } from "../chat-store";
import { Button } from "./ui/button";

function pretty(value: string) {
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
}
export function PartView({ part }: { part: Part }) {
  switch (part.part_type) {
    case "text":
      return (
        <div className="markdown">
          <Markdown remarkPlugins={[remarkGfm]}>{part.text ?? ""}</Markdown>
        </div>
      );
    case "reasoning":
      return (
        <details className="part-card reasoning">
          <summary>
            <Brain size={15} />
            <span>Thinking</span>
            <ChevronDown size={14} />
          </summary>
          <div className="part-content markdown">
            <Markdown>{part.text ?? ""}</Markdown>
          </div>
        </details>
      );
    case "tool_call":
      return (
        <details className="part-card">
          <summary>
            <Terminal size={15} />
            <span>{part.tool_call?.name ?? "Tool call"}</span>
            <span className="part-label">command</span>
            <ChevronDown size={14} />
          </summary>
          <pre className="part-content">{pretty(part.tool_call?.arguments ?? "")}</pre>
        </details>
      );
    case "tool_result":
      return (
        <details className={`part-card ${part.is_error ? "part-error" : ""}`}>
          <summary>
            {part.is_error ? <CircleAlert size={15} /> : <Check size={15} />}
            <span>{part.is_error ? "Tool error" : "Tool result"}</span>
            <ChevronDown size={14} />
          </summary>
          <pre className="part-content">{part.content ?? ""}</pre>
        </details>
      );
    case "media":
      return (
        <div className="media-reference">
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
        <details className="part-card part-error" open>
          <summary>
            <CircleAlert size={15} />
            <span>Unsupported part: {part.part_type}</span>
            <ChevronDown size={14} />
          </summary>
          <pre className="part-content">{JSON.stringify(part, null, 2)}</pre>
        </details>
      );
  }
}

export function MessageView({ message }: { message: ChatMessage }) {
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState(false);
  const user = message.role === "user";
  const text = message.parts
    .filter((p) => p.part_type === "text")
    .map((p) => p.text ?? "")
    .join("");
  return (
    <article
      id={`message-${message.key}`}
      className={`message ${user ? "user-message" : "agent-message"}`}
      data-message-key={message.key}
    >
      <div className="message-heading">
        <span className={`avatar ${user ? "user-avatar" : ""}`}>
          {user ? "You" : <Sparkles size={16} />}
        </span>
        <span className="message-author">
          {user ? "You" : message.role === "assistant" ? "q15" : message.role}
        </span>
        {message.model && <span className="model-tag">{message.model}</span>}
        <a
          className="message-time"
          href={`#message-${message.key}`}
          aria-label={`Link to message ${message.key}`}
        >
          {new Date(message.ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
        </a>
      </div>
      <div className="message-body">
        {message.parts.map((part, i) => (
          <PartView part={part} key={`${part.ordinal}:${i}`} />
        ))}
        {message.status === "streaming" && !text && (
          <span className="thinking-indicator">
            <span />
            <span />
            <span />
            <span className="sr-only">q15 is thinking</span>
          </span>
        )}
      </div>
      {!user && message.status !== "streaming" && (
        <div className="message-actions">
          <Button
            variant="ghost"
            size="sm"
            aria-label="Copy response"
            onClick={() => {
              void navigator.clipboard.writeText(text).then(
                () => {
                  setCopied(true);
                  setCopyError(false);
                },
                () => setCopyError(true),
              );
            }}
          >
            {copied ? <Check /> : <Copy />}
            {copied ? "Copied" : "Copy"}
          </Button>
          {copyError && <output>Copy unavailable. Select the text to copy.</output>}
          {message.status === "aborted" && <small>Stopped</small>}
          {message.status === "failed" && <small className="text-destructive">Failed</small>}
        </div>
      )}
    </article>
  );
}
