import { clsx } from "clsx";
import { Sparkles, Check, Copy } from "lucide-react";
import * as motion from "motion/react-m";
import { useState } from "react";

import type { ChatMessage, Pending } from "../domain/chat";

import { MediaView } from "./media";
import { PartView } from "./parts";
import { Button } from "./ui/button";
import { useMotionPreference } from "./ui/motion-preference";

import styles from "./message.module.css";

export function MessageView({ message }: { message: ChatMessage }) {
  const reduced = useMotionPreference();
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState(false);
  const user = message.role === "user";
  const text = message.parts
    .filter((p) => p.part_type === "text")
    .map((p) => p.text ?? "")
    .join("");
  const copyText = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setCopyError(false);
    } catch {
      setCopyError(true);
    }
  };
  return (
    <motion.article
      initial={!reduced && message.status === "streaming" ? { opacity: 0, y: 6 } : false}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: reduced ? 0 : 0.24 }}
      id={`message-${message.key}`}
      className={clsx(styles.message, user && styles.userMessage)}
      data-message-key={message.key}
    >
      <div className={styles.messageHeading}>
        <span className={clsx(styles.avatar, user && styles.userAvatar)} aria-hidden="true">
          {user ? "Y" : <Sparkles size={16} />}
        </span>
        <span className={styles.messageAuthor}>
          {user ? "You" : message.role === "assistant" ? "q15" : message.role}
        </span>
        {(message.model ?? "") !== "" && <span className={styles.modelTag}>{message.model}</span>}
        <a
          className={styles.messageTime}
          href={`#message-${message.key}`}
          aria-label={`Link to message ${message.key}`}
        >
          {new Date(message.ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
        </a>
      </div>
      <div className={styles.messageBody}>
        {message.parts.map((part) => (
          <PartView
            part={part}
            streaming={message.status === "streaming"}
            key={`${part.ordinal}:${part.part_type}:${part.tool_call?.id ?? part.tool_call_id ?? ""}`}
          />
        ))}
      </div>
      {!user &&
        message.status !== "streaming" &&
        (text !== "" || message.status === "aborted" || message.status === "failed") && (
          <div className={styles.messageActions}>
            {text !== "" && (
              <Button
                variant="ghost"
                size="sm"
                aria-label="Copy response"
                data-copied={copied || undefined}
                onClick={() => {
                  void copyText();
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
    </motion.article>
  );
}

export function PendingMessage({ pending: p }: { pending: Pending }) {
  const reduced = useMotionPreference();
  return (
    <motion.article
      className={clsx(styles.message, styles.userMessage)}
      initial={reduced ? false : { opacity: 0, y: 6 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: reduced ? 0 : 0.24 }}
    >
      <div className={styles.messageHeading}>
        <span className={clsx(styles.avatar, styles.userAvatar)} aria-hidden="true">
          Y
        </span>
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
                    ? p.turn === undefined
                      ? "Not accepted"
                      : "Response failed"
                    : "Sending…"}
        </span>
      </div>
      <div className={styles.messageBody}>
        <p className={styles.pendingText}>{p.text}</p>
        {p.parts?.map((part, ordinal) => (
          <MediaView key={part.media_ref} part={{ ...part, ordinal }} filename={part.filename} />
        ))}
      </div>
    </motion.article>
  );
}
