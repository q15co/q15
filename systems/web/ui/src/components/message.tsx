import { Sparkles, Check, Copy } from "lucide-react";
import { useState } from "react";
import { clsx } from "clsx";
import type { ChatMessage, Pending } from "../chat-store";
import { Button } from "./ui/button";
import { PartView } from "./parts";
import styles from "./message.module.css";

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
      className={clsx(styles.message, user && styles.userMessage)}
      data-message-key={message.key}
    >
      <div className={styles.messageHeading}>
        <span className={clsx(styles.avatar, user && styles.userAvatar)}>
          {user ? "You" : <Sparkles size={16} />}
        </span>
        <span className={styles.messageAuthor}>
          {user ? "You" : message.role === "assistant" ? "q15" : message.role}
        </span>
        {message.model && <span className={styles.modelTag}>{message.model}</span>}
        <a
          className={styles.messageTime}
          href={`#message-${message.key}`}
          aria-label={`Link to message ${message.key}`}
        >
          {new Date(message.ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
        </a>
      </div>
      <div className={styles.messageBody}>
        {message.parts.map((part, i) => (
          <PartView part={part} key={`${part.ordinal}:${i}`} />
        ))}
      </div>
      {!user &&
        message.status !== "streaming" &&
        (text || message.status === "aborted" || message.status === "failed") && (
          <div className={styles.messageActions}>
            {text && (
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
            )}
            {copyError && <output>Copy unavailable. Select the text to copy.</output>}
            {message.status === "aborted" && <small>Stopped</small>}
            {message.status === "failed" && <small className={styles.failed}>Failed</small>}
          </div>
        )}
    </article>
  );
}

export function PendingMessage({ pending: p }: { pending: Pending }) {
  return (
    <article className={clsx(styles.message, styles.userMessage)}>
      <div className={styles.messageHeading}>
        <span className={clsx(styles.avatar, styles.userAvatar)}>You</span>
        <span className={styles.messageAuthor}>You</span>
        <span className={clsx(styles.pendingLabel, p.state === "failed" && styles.failed)}>
          {p.state === "uncertain"
            ? "Delivery uncertain · check history before sending again"
            : p.state === "accepted" || p.state === "running" || p.state === "finished"
              ? "Sent"
              : p.state === "queued"
                ? "Queued"
                : p.state === "stopped"
                  ? "Stopped"
                  : p.state === "failed"
                    ? p.turn
                      ? "Response failed"
                      : "Not accepted"
                    : "Sending…"}
        </span>
      </div>
      <p className={clsx(styles.messageBody, styles.pendingText)}>{p.text}</p>
    </article>
  );
}
