import type { Transport, TransportEvents } from "../application/ports";
import type { ServerFrame } from "../domain/protocol";
import type { Page, Turn } from "../generated/protocol";

import { parseFrame, parsePage } from "../domain/protocol";
import aborted from "../fixtures/server/aborted.json";
import historyFixture from "../fixtures/server/history.json";
import resumed from "../fixtures/server/resumed.json";
import streamed from "../fixtures/server/streamed.json";
import { frame } from "./envelope";

// The offline preview replays frozen frames with fresh run/event identities.
export class MockTransport implements Transport {
  private events: TransportEvents | undefined;
  private timers: ReturnType<typeof setTimeout>[] = [];
  private running = false;
  private turn = 42;
  private eventIndex = 100;
  private text = "";
  private queued: { text: string; id: string }[] = [];
  private turns: Turn[] = parsePage(historyFixture).turns;
  history = (before: string): Promise<Page> =>
    Promise.resolve({
      turns: this.turns.filter((t) => before === "0" || BigInt(t.seq) < BigInt(before)),
      head_seq: String(this.turn),
      has_more: false,
    });
  start(events: TransportEvents) {
    this.events = events;
    events.connection("connected");
    for (const value of resumed) events.frame(parseFrame(JSON.stringify(value)));
    this.sync();
  }
  stop() {
    this.cancelTimers();
    this.events = undefined;
    this.running = false;
    this.queued = [];
  }
  private cancelTimers() {
    for (const timer of this.timers) clearTimeout(timer);
    this.timers = [];
  }
  private emit(type: string, payload: unknown) {
    this.events?.frame(parseFrame(JSON.stringify(frame(type, payload))));
  }
  send(text: string, id: string) {
    const queued = this.running;
    queueMicrotask(() =>
      this.emit("msg.status", {
        turn: String(this.turn),
        state: queued ? "queued" : "accepted",
        client_msg_id: id,
        queued,
      }),
    );
    if (queued) {
      this.queued.push({ text, id });
      return;
    }
    this.running = true;
    this.turn++;
    this.text = text;
    streamed.slice(1).forEach((value, i) => {
      this.timers.push(
        setTimeout(
          () => {
            const event = this.remap(parseFrame(JSON.stringify(value)));
            this.events?.frame(event);
            if (event.type === "msg.final") this.finish(event.payload.full_text);
          },
          (i + 1) * 400,
        ),
      );
    });
  }
  private remap(value: ServerFrame): ServerFrame {
    const event = structuredClone(value);
    event.id = `preview:${++this.eventIndex}`;
    event.seq = String(this.eventIndex);
    if (event.type === "turn.start") event.payload.turn = String(this.turn);
    if ("msg" in event.payload) event.payload.msg.turn = String(this.turn);
    return event;
  }
  abort() {
    this.cancelTimers();
    const event = this.remap(parseFrame(JSON.stringify(aborted)));
    this.events?.frame(event);
    this.finish(aborted.payload.full_text);
  }
  private finish(answer: string) {
    this.turns.unshift({
      seq: String(this.turn),
      created_at: new Date().toISOString(),
      messages: [
        { ordinal: 0, role: "user", parts: [{ ordinal: 0, part_type: "text", text: this.text }] },
        {
          ordinal: 1,
          role: "assistant",
          parts: [
            { ordinal: 0, part_type: "reasoning", text: "thinking" },
            { ordinal: 1, part_type: "text", text: answer },
          ],
        },
      ],
    });
    this.running = false;
    const next = this.queued.shift();
    if (next) this.send(next.text, next.id);
  }
  sync() {
    this.emit("ready", {
      head_seq: String(this.turn),
      cursor: this.turns[0]?.seq ?? "0",
      device_id: "preview",
    });
  }
  presence() {}
}
