import { clsx } from "clsx";
import { Check, Copy } from "lucide-react";
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
  const MessageElement = user ? motion.article : motion.div;
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
    <MessageElement
      initial={!reduced && message.status === "streaming" ? { opacity: 0 } : false}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: reduced ? 0 : 0.24 }}
      id={`message-${message.key}`}
      className={clsx(styles.message, user ? styles.userMessage : styles.answer)}
      data-message-key={message.key}
      data-user-turn={user || undefined}
      aria-label={user ? "Your message" : undefined}
    >
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
    </MessageElement>
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
      data-user-turn
      aria-label="Your message"
    >
      <div className={styles.messageBody}>
        <p className={styles.pendingText}>{p.text}</p>
        {p.parts?.map((part, ordinal) => (
          <MediaView key={part.media_ref} part={{ ...part, ordinal }} filename={part.filename} />
        ))}
      </div>
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
    </motion.article>
  );
}
