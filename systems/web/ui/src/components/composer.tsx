import { ArrowUp, Square, CornerDownLeft } from "lucide-react";
import { useRef, useState } from "react";
import type { ChatStore, ChatState } from "../chat-store";
import { Button } from "./ui/button";

export function Composer({ store, state }: { store: ChatStore; state: ChatState }) {
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
    <div className="composer-area">
      <form
        className="composer"
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
          rows={3}
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
        <div className="composer-toolbar">
          <span className="composer-hint">
            {busy ? "Your next message joins the queue" : "A little curiosity goes a long way"}
          </span>
          <div className="composer-buttons">
            {busy && (
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
            )}
            <Button
              type="submit"
              size="icon"
              disabled={!text.trim() || state.connection !== "connected"}
              aria-label={busy ? "Queue message" : "Send message"}
            >
              <ArrowUp />
            </Button>
          </div>
        </div>
      </form>
      <div className="composer-footer">
        <span>q15 · your personal agent</span>
        <span className="keyboard-hint">
          <CornerDownLeft size={12} /> send<span>Shift + Enter for a new line</span>
        </span>
      </div>
    </div>
  );
}
