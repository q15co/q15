import styles from "./composer.module.css";
import { ArrowUp, ListPlus, Square, CornerDownLeft } from "lucide-react";
import { AnimatePresence } from "motion/react";
import * as m from "motion/react-m";
import { useRef, useState } from "react";
import type { ChatStore, ChatState } from "../chat-store";
import { Button } from "./ui/button";
import { useMotionPreference } from "./ui/motion";

export function Composer({ store, state }: { store: ChatStore; state: ChatState }) {
  const reduced = useMotionPreference();
  const [text, setText] = useState("");
  const input = useRef<HTMLTextAreaElement>(null);
  const submit = () => {
    if (store.send(text)) {
      setText("");
      input.current?.focus();
    }
  };
  const busy =
    state.active !== null ||
    state.pending.some((p) => p.state === "sending" || p.state === "accepted");
  return (
    <div className={styles.composerArea}>
      <form
        className={styles.composer}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <label htmlFor="message-input" className="sr-only">
          Message q15
        </label>
        <textarea
          id="message-input"
          ref={input}
          value={text}
          rows={1}
          placeholder="What's on your mind?"
          onChange={(event) => setText(event.target.value)}
          onKeyDown={(event) => {
            if (
              event.key === "Enter" &&
              !event.shiftKey &&
              !event.nativeEvent.isComposing &&
              !matchMedia("(pointer: coarse)").matches
            ) {
              event.preventDefault();
              submit();
            }
          }}
        />
        <div className={styles.composerToolbar}>
          <span className={styles.composerHint}>
            {busy ? "Your next message joins the queue" : "A little curiosity goes a long way"}
          </span>
          <div className={styles.composerButtons}>
            {busy && (
              <m.span
                className={styles.stopControl}
                initial={reduced ? false : { opacity: 0 }}
                animate={{ opacity: 1 }}
                transition={{ duration: reduced ? 0 : 0.15 }}
              >
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => store.abort()}
                  disabled={state.connection !== "connected"}
                  aria-label="Stop response"
                >
                  <Square />
                  Stop
                </Button>
              </m.span>
            )}
            <Button
              type="submit"
              size="icon"
              disabled={!text.trim() || state.connection !== "connected"}
              aria-label={busy ? "Queue message" : "Send message"}
            >
              <AnimatePresence initial={false} mode="wait">
                <m.span
                  key={busy ? "queue" : "send"}
                  aria-hidden="true"
                  initial={reduced ? false : { opacity: 0, y: 4 }}
                  animate={{ opacity: 1, y: 0 }}
                  exit={{ opacity: 0, y: reduced ? 0 : -4 }}
                  transition={{ duration: reduced ? 0 : 0.12 }}
                >
                  {busy ? <ListPlus /> : <ArrowUp />}
                </m.span>
              </AnimatePresence>
            </Button>
          </div>
        </div>
      </form>
      <div className={styles.composerFooter}>
        <span>q15 · your personal agent</span>
        <span className={styles.keyboardHint}>
          <CornerDownLeft size={12} /> send<span>Shift + Enter for a new line</span>
        </span>
      </div>
    </div>
  );
}
