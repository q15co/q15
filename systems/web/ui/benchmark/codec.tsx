import "@fontsource-variable/recursive/full.css";
import { createRoot } from "react-dom/client";

import type { Transport } from "../src/application/ports";
import type { Page } from "../src/generated/protocol";

import { App } from "../src/app";
import { ChatStore } from "../src/application/chat-store";
import { MotionProvider } from "../src/components/ui/motion";
import { clientFrame } from "../src/infrastructure/envelope";
import { loadedHistory } from "../src/testing/streaming";
import { codec } from "./codec-adapter";

import "../src/styles.css";

const params = new URLSearchParams(location.search);
const rendered = params.has("rendered");
const count = rendered ? 1000 : 0;
const paging = params.has("paging");
let running = false;
const transport: Transport = {
  start: (events) => events.connection("connected"),
  stop: () => {},
  send: () => {},
  abort: () => {},
  sync: () => {},
  presence: () => {},
};
const initial = paging
  ? { ...loadedHistory(2000), turns: loadedHistory(2000).turns.slice(0, count) }
  : loadedHistory(count);
const store = new ChatStore(transport, (before) =>
  running && paging && before !== "0" ? pageHistory() : Promise.resolve(initial),
);
if (rendered) {
  const root = document.querySelector("#root");
  if (root === null) throw new Error("Missing benchmark root");
  createRoot(root).render(
    <MotionProvider>
      <App store={store} />
    </MotionProvider>,
  );
  await store.loadHistory(true);
  while (root.querySelectorAll("article[data-message-key]").length !== count)
    await new Promise<void>((done) => {
      requestAnimationFrame(() => done());
    });
  await document.fonts.ready;
  await new Promise<void>((done) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => done());
    });
  });
}
const hello = await codec.offer("0", "b".repeat(43));
const response = await fetch(`/benchmark/setup?${params}`, { method: "POST", body: hello });
const fixture: unknown = await response.json();
if (
  typeof fixture !== "object" ||
  fixture === null ||
  !("key" in fixture) ||
  !("frames" in fixture) ||
  !("history" in fixture) ||
  !("paging" in fixture) ||
  !("final" in fixture) ||
  typeof fixture.key !== "string" ||
  typeof fixture.history !== "string" ||
  typeof fixture.paging !== "string" ||
  typeof fixture.final !== "string" ||
  !Array.isArray(fixture.frames) ||
  !fixture.frames.every((data: unknown) => typeof data === "string")
)
  throw new Error("Invalid benchmark fixture");
const wires: string[] = fixture.frames;
await codec.receive(fixture.key);
const channel = await codec.channel();
const historyBytes = new TextEncoder().encode(fixture.history).buffer;
const historyByteLength = historyBytes.byteLength;
const pagingBytes = new TextEncoder().encode(fixture.paging).buffer;
const finalWire = fixture.final;
const arrivals: number[] = [];
const processing: number[] = [];
const latency: number[] = [];
let historyArrival: number | undefined;
let historyCompletionMs: number | undefined;
let historyVisibleMs: number | undefined;
let terminalArrival = 0;
let terminalVisibleMs: number | undefined;
let historyJob: Promise<void> | undefined;
async function pageHistory(): Promise<Page> {
  historyArrival = performance.now();
  const result = await codec.history(pagingBytes, channel);
  historyCompletionMs = performance.now() - historyArrival;
  return result;
}
const nextFrame = () =>
  new Promise<void>((done) => {
    requestAnimationFrame(() => done());
  });
const drain = async (wire: string, index: number) => {
  arrivals[index] = performance.now();
  const result = await codec.receive(wire);
  processing.push(performance.now() - arrivals[index]);
  if (result.kind !== "frame") throw new Error("Codec did not return a domain frame");
  if (rendered) store.consume(result.frame);
};
document.addEventListener(
  "benchmark-start",
  () => {
    void run();
  },
  { once: true },
);
async function run() {
  running = true;
  const longTasks: number[] = [];
  const observer = new PerformanceObserver((list) => {
    for (const entry of list.getEntries()) longTasks.push(entry.duration);
  });
  observer.observe({ type: "longtask" });
  performance.clearMarks();
  performance.clearMeasures();
  let visible = -1;
  let observing = rendered;
  const sample = () => {
    const text = document.querySelector('article[data-message-key="10001:-1"]')?.textContent ?? "";
    const match = [...text.matchAll(/frame-(\d+)\./gu)].at(-1);
    const latest = match?.[1] === undefined ? -1 : Number(match[1]);
    const now = performance.now();
    for (; visible < latest; visible++) latency.push(now - (arrivals[visible + 1] ?? now));
    if (terminalArrival > 0 && text.includes("Stopped"))
      terminalVisibleMs ??= now - terminalArrival;
    if (
      historyArrival !== undefined &&
      document.querySelector('[data-message-key="1000:0"]') !== null
    )
      historyVisibleMs ??= now - historyArrival;
    if (observing) requestAnimationFrame(sample);
  };
  if (rendered) requestAnimationFrame(sample);
  const started = performance.now();
  if (params.has("history")) await codec.history(historyBytes, channel);
  if (params.has("outgoing"))
    for (let index = 0; index < 48; index++)
      await codec.send(
        clientFrame("msg.send", { client_msg_id: String(index), text: "x".repeat(64000) }),
      );
  if (rendered) {
    const received: Promise<void>[] = [];
    for (let index = 0; index < wires.length; index++) {
      // Record arrival independently of completion, as a socket does.
      received.push(drain(wires[index] ?? "", index));
      if (paging && index === 6) historyJob = store.loadHistory();
      await new Promise<void>((done) => {
        setTimeout(done, 16);
      });
    }
    await Promise.all(received);
    await historyJob;
    terminalArrival = performance.now();
    const terminal = await codec.receive(finalWire);
    if (terminal.kind !== "frame") throw new Error("Missing terminal frame");
    store.consume(terminal.frame);
    await nextFrame();
    await nextFrame();
    const deadline = performance.now() + 10_000;
    while (
      !(document.querySelector('article[data-message-key="10001:-1"]')?.textContent ?? "").includes(
        `frame-${wires.length - 1}.`,
      )
    ) {
      if (performance.now() > deadline) throw new Error("Final frame not visible");
      await nextFrame();
    }
    sample();
  } else {
    await Promise.all(wires.map((wire, index) => drain(wire, index)));
  }
  const elapsedMs = performance.now() - started;
  observing = false;
  await new Promise<void>((done) => {
    setTimeout(done, 100);
  });
  observer.disconnect();
  const result = document.createElement("output");
  result.dataset.benchmarkResult = JSON.stringify({
    rendered,
    history: count,
    frames: wires.length,
    elapsedMs,
    longTasks,
    latency,
    processing,
    arrivals: arrivals.map((arrival) => arrival - started),
    terminalVisibleMs,
    streamingTailMs: latency.at(-1),
    historyCompletionMs,
    historyVisibleMs,
    work: {
      markdown: performance.getEntriesByName("q15-markdown").map((entry) => entry.duration),
      liveRenders: performance.getEntriesByName("q15-live-render").length,
      historyRenders: performance.getEntriesByName("q15-history-render").length,
    },
    wireBytes: wires.reduce((sum, data) => sum + data.length, 0),
    historyBytes: historyByteLength,
  });
  document.body.append(result);
  codec.reset();
}
document.documentElement.dataset.benchmarkReady = "true";
