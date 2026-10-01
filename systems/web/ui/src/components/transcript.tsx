import { ArrowDown, History, Sparkles, ArrowUpRight } from "lucide-react";
import { useLayoutEffect, useRef, useState } from "react";
import type { ChatState, ChatStore } from "../chat-store";
import { MessageView } from "./message";
import { Button } from "./ui/button";

export function Transcript({ state, store }: { state: ChatState; store: ChatStore }) {
  const scroller = useRef<HTMLDivElement>(null);
  const anchor = useRef<{
    key: string;
    top: number;
    scrollHeight: number;
  } | null>(null);
  const following = useRef(true);
  const [showLatest, setShowLatest] = useState(false);
  const [jumping, setJumping] = useState(false);
  const loadOlder = () => {
    const node = scroller.current;
    if (!node || !state.hasMore || state.loadingHistory || jumping) return;
    const first = [...node.querySelectorAll<HTMLElement>("[data-message-key]")].find(
      (item) => item.getBoundingClientRect().bottom >= node.getBoundingClientRect().top,
    );
    if (first)
      anchor.current = {
        key: first.dataset.messageKey!,
        top: first.getBoundingClientRect().top,
        scrollHeight: node.scrollHeight,
      };
    following.current = false;
    void store.loadHistory();
  };

  useLayoutEffect(() => {
    const node = scroller.current;
    if (!node) return;
    if (anchor.current && !state.loadingHistory) {
      const saved = anchor.current;
      const item = [...node.querySelectorAll<HTMLElement>("[data-message-key]")].find(
        (item) => item.dataset.messageKey === saved.key,
      );
      node.scrollTop += item
        ? item.getBoundingClientRect().top - saved.top
        : node.scrollHeight - saved.scrollHeight;
      anchor.current = null;
    } else if (following.current && !anchor.current) node.scrollTop = node.scrollHeight;
  }, [state.messages, state.pending, state.loadingHistory]);

  useLayoutEffect(() => {
    const jump = async () => {
      const match = /^#message-(\d+):(-?\d+)$/.exec(location.hash);
      if (!match || state.loadingHistory) return;
      following.current = false;
      setJumping(true);
      const key = await store.findMessage(match[1]!, Number(match[2]));
      setJumping(false);
      if (key)
        requestAnimationFrame(() =>
          document.getElementById(`message-${key}`)?.scrollIntoView({ block: "center" }),
        );
    };
    void jump();
    window.addEventListener("hashchange", jump);
    return () => window.removeEventListener("hashchange", jump);
  }, [store, state.hasMore, state.loadingHistory]);

  return (
    <div className="transcript-container">
      <div
        className="transcript"
        ref={scroller}
        aria-label="Conversation"
        onScroll={() => {
          const node = scroller.current!;
          following.current = node.scrollHeight - node.scrollTop - node.clientHeight < 100;
          setShowLatest(!following.current);
          if (node.scrollTop < 80 && !following.current) loadOlder();
        }}
      >
        <div className="transcript-inner">
          {state.hasMore && (
            <div className="history-control">
              <Button variant="ghost" size="sm" disabled={state.loadingHistory} onClick={loadOlder}>
                <History />
                {state.loadingHistory ? "Loading earlier messages…" : "Earlier messages"}
              </Button>
            </div>
          )}
          {state.messages.length === 0 && state.pending.length === 0 && (
            <div className="welcome">
              <div className="welcome-mark">
                <Sparkles />
              </div>
              <span className="eyebrow">A SPACE FOR YOUR IDEAS</span>
              <h1>
                Where shall we
                <br />
                <span>begin?</span>
              </h1>
              <p>
                A question, a plan, a half-formed thought.
                <br />
                Bring it here. We'll work it out together.
              </p>
              <div className="suggestions">
                {[
                  "Help me think through an idea",
                  "Explore something new",
                  "Let's get something done",
                ].map((text) => (
                  <button
                    key={text}
                    onClick={() => store.send(text)}
                    disabled={state.connection !== "connected"}
                  >
                    <span>{text}</span>
                    <ArrowUpRight size={16} />
                  </button>
                ))}
              </div>
            </div>
          )}
          {state.messages.map((message) => (
            <MessageView key={message.key} message={message} />
          ))}
          {state.pending.map((p) => (
            <article className="message user-message pending-message" key={p.id}>
              <div className="message-heading">
                <span className="avatar user-avatar">You</span>
                <span className="message-author">You</span>
                <span className={`pending-label ${p.state === "failed" ? "text-destructive" : ""}`}>
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
              <p className="message-body whitespace-pre-wrap">{p.text}</p>
            </article>
          ))}
          {jumping && <output>Finding your message…</output>}
        </div>
      </div>
      {showLatest && (
        <Button
          className="latest-button"
          variant="outline"
          size="sm"
          onClick={() => {
            following.current = true;
            scroller.current?.scrollTo({ top: scroller.current.scrollHeight, behavior: "smooth" });
          }}
        >
          <ArrowDown />
          Back to latest
        </Button>
      )}
    </div>
  );
}
