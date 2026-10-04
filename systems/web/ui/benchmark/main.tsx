import "@fontsource-variable/recursive/full.css";
import { createRoot } from "react-dom/client";

import type { Transport } from "../src/application/ports";

import { App } from "../src/app";
import { ChatStore } from "../src/application/chat-store";
import { MotionProvider } from "../src/components/ui/motion";
import { parseFrame } from "../src/domain/protocol";
import { loadedHistory, growingAnswer } from "../src/testing/streaming";

import "../src/styles.css";

const root = document.querySelector("#root");
if (root === null) throw new Error("Missing benchmark root");
const params = new URLSearchParams(location.search);
const count = Number(params.get("history") ?? "1000");
const large = params.has("large");
if (params.has("staticAxes")) {
  const style = document.createElement("style");
  style.textContent =
    '[data-agent-activity] [class*="activityLabel"] { font-variation-settings: "MONO" 0, "CASL" 0.2, "wght" 500, "slnt" 0, "CRSV" 0 !important; }';
  document.head.append(style);
}
const frames = large ? 120 : 24;
const cadence = 16;
const answer = growingAnswer(large ? 600 : 30);
const transport: Transport = {
  start: (events) => events.connection("connected"),
  stop: () => {},
  send: () => {},
  abort: () => {},
  sync: () => {},
  presence: () => {},
};
const store = new ChatStore(transport, () => Promise.resolve(loadedHistory(count)));
createRoot(root).render(
  <MotionProvider>
    <App store={store} />
  </MotionProvider>,
);

const nextFrame = () =>
  new Promise<void>((resolve) => {
    requestAnimationFrame(() => resolve());
  });
const send = (type: string, payload: unknown) =>
  store.consume(
    parseFrame(
      JSON.stringify({
        v: 2,
        id: crypto.randomUUID(),
        ts: "2026-10-01T12:00:00Z",
        seq: "0",
        type,
        payload,
      }),
    ),
  );
const msg = { turn: "10001", ordinal: -1 };

document.addEventListener(
  "benchmark-start",
  () => {
    void run();
  },
  { once: true },
);
async function run() {
  const longTasks: number[] = [];
  const observer = new PerformanceObserver((list) => {
    for (const entry of list.getEntries()) longTasks.push(entry.duration);
  });
  observer.observe({ type: "longtask" });
  const arrivals: number[] = [];
  const latency: number[] = [];
  let visible = -1;
  let observing = true;
  const sample = () => {
    const text = document.querySelector('article[data-message-key="10001:-1"]')?.textContent ?? "";
    const match = [...text.matchAll(/frame-(\d+)\./gu)].at(-1);
    const latest = match?.[1] === undefined ? -1 : Number(match[1]);
    const now = performance.now();
    for (; visible < latest; visible++) latency.push(now - (arrivals[visible + 1] ?? now));
    if (observing) requestAnimationFrame(sample);
  };
  requestAnimationFrame(sample);
  send("turn.start", { turn: msg.turn, msg });
  send("snapshot", { msg, seq: "1", kind: "model_start", text: "" });
  send("delta", {
    msg,
    seq: "2",
    kind: "tool_call",
    text: "",
    call: { id: "active-tool", name: "exec", arguments: '{"command":"active tool"}' },
  });
  send("delta", {
    msg,
    seq: "3",
    kind: "tool_result",
    text: "Done.",
    call: { id: "active-tool", name: "exec", arguments: "{}" },
  });
  let full = "";
  const started = performance.now();
  for (let index = 0; index < frames; index++) {
    const start = Math.floor((index * answer.length) / frames);
    const end = Math.floor(((index + 1) * answer.length) / frames);
    const chunk = answer.slice(start, end) + `\n\nframe-${index}.\n\n`;
    full += chunk;
    arrivals.push(performance.now());
    send(index % 10 === 0 ? "snapshot" : "delta", {
      msg,
      seq: String(index + 4),
      kind: "text",
      text: index % 10 === 0 ? full : chunk,
    });
    send(index % 10 === 0 ? "snapshot" : "delta", {
      msg,
      seq: String(index + frames + 4),
      kind: "reasoning",
      text: index % 10 === 0 ? `Reasoning ${index}` : ".",
    });
    await new Promise<void>((resolve) => {
      setTimeout(resolve, cadence);
    });
  }
  send("msg.final", { msg, status: "aborted", full_text: full });
  // Completion changes the message identity, so include any remaining frames in its visible flush.
  await nextFrame();
  await nextFrame();
  const finalText =
    document.querySelector('article[data-message-key="10001:-1"]')?.textContent ?? "";
  if (!finalText.includes(`frame-${frames - 1}.`)) throw new Error("Final content was lost");
  const now = performance.now();
  for (; visible < frames - 1; visible++) latency.push(now - (arrivals[visible + 1] ?? now));
  observing = false;
  await new Promise<void>((resolve) => {
    setTimeout(resolve, 100);
  });
  observer.disconnect();
  const result = document.createElement("output");
  result.dataset.benchmarkResult = JSON.stringify({
    history: count,
    frames,
    cadence,
    activeBytes: new TextEncoder().encode(full).length,
    elapsedMs: now - started,
    longTasks,
    latency,
    arrivals,
  });
  document.body.append(result);
}

await new Promise<void>((resolve) => {
  const unsubscribe = store.subscribe(() => {
    if (!store.getSnapshot().loadingHistory && store.getSnapshot().messages.length === count) {
      unsubscribe();
      resolve();
    }
  });
});
while (root.querySelectorAll("article[data-message-key]").length !== count) await nextFrame();
await nextFrame();
await document.fonts.ready;
await nextFrame();
await nextFrame();
document.documentElement.dataset.benchmarkReady = "true";
