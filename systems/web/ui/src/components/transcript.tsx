import { ArrowDown, History } from "lucide-react";
import { AnimatePresence } from "motion/react";
import * as motion from "motion/react-m";
import { Fragment, memo, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";

import type { ChatStore } from "../application/chat-store";
import type { ChatMessage, ChatState, Pending } from "../domain/chat";

import { groupTurns } from "../domain/chat";
import { TurnView } from "./activity";
import { PendingMessage } from "./message";
import { Button } from "./ui/button";
import { useMotionPreference } from "./ui/motion-preference";
import { Welcome } from "./welcome";

import styles from "./transcript.module.css";

const Turn = memo(
  function Turn({
    turn,
    messages,
    active,
    store,
  }: {
    turn: string;
    messages: readonly ChatMessage[];
    active: boolean;
    store: ChatStore;
  }) {
    const live = useSyncExternalStore(
      (listener) => store.subscribeTurn(turn, listener),
      () => store.getLive(turn),
    );
    const combined = live === null ? messages : [...messages, live];
    return (
      <TurnView
        messages={combined}
        working={active || combined.some((m) => m.status === "streaming")}
      />
    );
  },
  (previous, next) =>
    previous.turn === next.turn &&
    previous.active === next.active &&
    previous.store === next.store &&
    previous.messages.length === next.messages.length &&
    previous.messages.every((message, index) => message === next.messages[index]),
);

function Turns({
  messages,
  active,
  liveTurn,
  pending,
  store,
}: {
  messages: readonly ChatMessage[];
  active: string | null;
  liveTurn: string | null;
  pending: readonly Pending[];
  store: ChatStore;
}) {
  const turns = groupTurns(messages, active ?? liveTurn);
  return (
    <>
      {[...turns].map(([turn, group]) => (
        <Fragment key={turn}>
          {pending
            .filter((p) => p.turn === turn)
            .map((p) => (
              <PendingMessage key={p.id} pending={p} />
            ))}
          <Turn turn={turn} messages={group} active={active === turn} store={store} />
        </Fragment>
      ))}
      {pending
        .filter((p) => p.turn === undefined || !turns.has(p.turn))
        .map((p) => (
          <PendingMessage key={p.id} pending={p} />
        ))}
    </>
  );
}

function followTop(scroller: HTMLElement, content: HTMLElement | null) {
  const turn = content?.lastElementChild;
  const first =
    turn instanceof HTMLElement && Object.hasOwn(turn.dataset, "agentTurn")
      ? turn.querySelector<HTMLElement>(":scope > [data-message-key]")
      : null;
  const last = turn?.lastElementChild;
  if (!first || !last) return scroller.scrollHeight;
  const start = first.getBoundingClientRect().top;
  if (last.getBoundingClientRect().bottom - start <= scroller.clientHeight)
    return scroller.scrollHeight;
  return scroller.scrollTop + start - scroller.getBoundingClientRect().top;
}

export function Transcript({ state, store }: { state: ChatState; store: ChatStore }) {
  const reduced = useMotionPreference();
  const scroller = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const anchor = useRef<{
    key: string;
    top: number;
    scrollHeight: number;
  } | null>(null);
  const following = useRef(true);
  const followedTop = useRef(0);
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
    if (first?.dataset.messageKey !== undefined)
      anchor.current = {
        key: first.dataset.messageKey,
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
        (candidate) => candidate.dataset.messageKey === saved.key,
      );
      node.scrollTop += item
        ? item.getBoundingClientRect().top - saved.top
        : node.scrollHeight - saved.scrollHeight;
      anchor.current = null;
    }
  }, [state.loadingHistory]);

  // Keep tall answers readable from their beginning while content grows.
  // A reader scrolling away or paging history owns their position.
  useLayoutEffect(() => {
    const element = content.current;
    let frame: number | undefined;
    const follow = () => {
      frame = undefined;
      const node = scroller.current;
      if (node && following.current && !anchor.current) {
        node.scrollTop = followTop(node, element);
        followedTop.current = node.scrollTop;
      }
    };
    const observer =
      element && typeof ResizeObserver !== "undefined"
        ? new ResizeObserver(() => {
            frame ??= requestAnimationFrame(follow);
          })
        : undefined;
    if (element) observer?.observe(element);
    return () => {
      observer?.disconnect();
      if (frame !== undefined) cancelAnimationFrame(frame);
    };
  }, []);

  useLayoutEffect(() => {
    const jump = async () => {
      const match = /^#message-(\d+):(-?\d+)$/u.exec(location.hash);
      if (match?.[1] === undefined || match[2] === undefined || state.loadingHistory) return;
      following.current = false;
      setJumping(true);
      const key = await store.findMessage(match[1], Number(match[2]));
      setJumping(false);
      if (key !== null)
        requestAnimationFrame(() => {
          const target = document.querySelector(`#${CSS.escape(`message-${key}`)}`);
          const disclosures: HTMLDetailsElement[] = [];
          for (let parent = target; parent; parent = parent.parentElement)
            if (parent instanceof HTMLDetailsElement) {
              parent.dataset.instant = "";
              parent.open = true;
              disclosures.push(parent);
            }
          target?.scrollIntoView({ block: "center" });
          requestAnimationFrame(() => {
            requestAnimationFrame(() => {
              for (const details of disclosures) delete details.dataset.instant;
            });
          });
        });
    };
    const onHashChange = () => {
      void jump();
    };
    onHashChange();
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, [store, state.loadingHistory]);

  return (
    <div className={styles.transcriptContainer}>
      <div
        className={styles.transcript}
        ref={scroller}
        aria-label="Conversation"
        onScroll={(event) => {
          const node = event.currentTarget;
          const atBottom = node.scrollHeight - node.scrollTop - node.clientHeight < 100;
          // A queued scroll event can arrive after content grows but before its
          // ResizeObserver callback. Keep following when our last scroll position
          // is unchanged; only the reader moving away should interrupt it.
          if (atBottom) {
            following.current = true;
            followedTop.current = node.scrollTop;
          } else if (Math.abs(node.scrollTop - followedTop.current) > 1) following.current = false;
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
          {state.messages.length === 0 && state.live === null && state.pending.length === 0 && (
            <Welcome store={store} connected={state.connection === "connected"} />
          )}
          <Turns
            messages={state.messages}
            active={state.active}
            liveTurn={state.live?.turn ?? null}
            pending={state.pending}
            store={store}
          />
          {jumping && <output>Finding your message…</output>}
        </div>
      </div>
      <div className={styles.latestWrapper}>
        <AnimatePresence initial={false}>
          {showLatest && (
            <motion.div
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
                  const node = scroller.current;
                  if (!node) return;
                  following.current = true;
                  const top = followTop(node, content.current);
                  node.scrollTo({
                    top,
                    behavior: reduced || top !== node.scrollHeight ? "auto" : "smooth",
                  });
                  followedTop.current = node.scrollTop;
                  setShowLatest(false);
                }}
              >
                <ArrowDown />
                Back to latest
              </Button>
            </motion.div>
          )}
        </AnimatePresence>
      </div>
    </div>
  );
}
