import styles from "./transcript.module.css";
import { ArrowDown, History } from "lucide-react";
import { AnimatePresence } from "motion/react";
import * as m from "motion/react-m";
import { Fragment, useLayoutEffect, useRef, useState } from "react";
import type { ChatState, ChatStore } from "../chat-store";
import { PendingMessage } from "./message";
import { TurnView } from "./activity";
import { Button } from "./ui/button";
import { useMotionPreference } from "./ui/motion";
import { Welcome } from "./welcome";

export function Transcript({ state, store }: { state: ChatState; store: ChatStore }) {
  const reduced = useMotionPreference();
  const turns = new Map<string, typeof state.messages>();
  for (const message of state.messages) {
    const messages = turns.get(message.turn) ?? [];
    messages.push(message);
    turns.set(message.turn, messages);
  }
  const scroller = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
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
      (item) =>
        item.getClientRects().length > 0 &&
        item.getBoundingClientRect().bottom >= node.getBoundingClientRect().top,
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

  // Follow expanding disclosures as well as text deltas, without moving a reader
  // who has scrolled back or is paging history.
  useLayoutEffect(() => {
    if (!content.current || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => {
      const node = scroller.current;
      if (node && following.current && !anchor.current) node.scrollTop = node.scrollHeight;
    });
    observer.observe(content.current);
    return () => observer.disconnect();
  }, []);

  useLayoutEffect(() => {
    const jump = async () => {
      const match = /^#message-(\d+):(-?\d+)$/.exec(location.hash);
      if (!match || state.loadingHistory) return;
      following.current = false;
      setJumping(true);
      const key = await store.findMessage(match[1]!, Number(match[2]));
      setJumping(false);
      if (key)
        requestAnimationFrame(() => {
          const target = document.getElementById(`message-${key}`);
          const disclosures: HTMLDetailsElement[] = [];
          for (let parent = target; parent; parent = parent.parentElement)
            if (parent instanceof HTMLDetailsElement) {
              parent.dataset.instant = "";
              parent.open = true;
              disclosures.push(parent);
            }
          target?.scrollIntoView({ block: "center" });
          requestAnimationFrame(() =>
            requestAnimationFrame(() => {
              disclosures.forEach((details) => delete details.dataset.instant);
            }),
          );
        });
    };
    void jump();
    window.addEventListener("hashchange", jump);
    return () => window.removeEventListener("hashchange", jump);
  }, [store, state.hasMore, state.loadingHistory]);

  return (
    <div className={styles.transcriptContainer}>
      <div
        className={styles.transcript}
        ref={scroller}
        aria-label="Conversation"
        onScroll={() => {
          const node = scroller.current!;
          following.current = node.scrollHeight - node.scrollTop - node.clientHeight < 100;
          setShowLatest(!following.current);
          if (node.scrollTop < 80 && !following.current) loadOlder();
        }}
      >
        <div className={styles.transcriptInner} ref={content}>
          {state.hasMore && (
            <div className={styles.historyControl}>
              <Button variant="ghost" size="sm" disabled={state.loadingHistory} onClick={loadOlder}>
                <History />
                {state.loadingHistory ? "Loading earlier messages…" : "Earlier messages"}
              </Button>
            </div>
          )}
          {state.messages.length === 0 && state.pending.length === 0 && (
            <Welcome store={store} connected={state.connection === "connected"} />
          )}
          {[...turns].map(([turn, messages]) => (
            <Fragment key={turn}>
              {state.pending
                .filter((p) => p.turn === turn)
                .map((p) => (
                  <PendingMessage key={p.id} pending={p} />
                ))}
              <TurnView
                messages={messages}
                working={state.active === turn || messages.some((m) => m.status === "streaming")}
              />
            </Fragment>
          ))}
          {state.pending
            .filter((p) => !p.turn || !turns.has(p.turn))
            .map((p) => (
              <PendingMessage key={p.id} pending={p} />
            ))}
          {jumping && <output>Finding your message…</output>}
        </div>
      </div>
      <div className={styles.latestWrapper}>
        <AnimatePresence initial={false}>
          {showLatest && (
            <m.div
              initial={reduced ? false : { opacity: 0, y: 8 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, y: reduced ? 0 : 8 }}
              transition={{ duration: reduced ? 0 : 0.2 }}
            >
              <Button
                className={styles.latestButton}
                variant="outline"
                size="sm"
                onClick={() => {
                  following.current = true;
                  scroller.current?.scrollTo({
                    top: scroller.current.scrollHeight,
                    behavior: reduced ? "auto" : "smooth",
                  });
                }}
              >
                <ArrowDown />
                Back to latest
              </Button>
            </m.div>
          )}
        </AnimatePresence>
      </div>
    </div>
  );
}
