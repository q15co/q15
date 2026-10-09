import type { Page as BrowserPage } from "@playwright/test";

import { expect, test } from "@playwright/test";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";

import type { ClientFrame } from "../src/domain/protocol";
import type { Attachment, Frame, Page } from "../src/generated/protocol";

import { parseMediaFiles } from "../src/domain/media";
import { parseClientFrame, parseSealed, parseWireFrame } from "../src/domain/protocol";
import { frame } from "../src/infrastructure/envelope";
import { parseShellManifest } from "../src/shared/shell-manifest";
import { required } from "../src/testing/required";
import { TestSealer } from "./seal";

test.beforeEach(async ({ page }) => {
  await page.route("**/session-key-setup", (route) =>
    route.fulfill({ contentType: "text/html", body: "<!doctype html><title>Setup</title>" }),
  );
  await page.goto("/session-key-setup");
  await page.evaluate(async () => {
    const pair = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, [
      "sign",
      "verify",
    ]);
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.open("q15-session-key", 1);
      request.addEventListener("upgradeneeded", () => {
        request.result.createObjectStore("signer");
      });
      request.addEventListener("error", () => reject(new Error("Session setup failed")));
      request.addEventListener("success", () => {
        const db = request.result;
        const transaction = db.transaction("signer", "readwrite");
        transaction
          .objectStore("signer")
          .put({ key: pair.privateKey, binding: "a".repeat(43) }, "current");
        transaction.addEventListener("complete", () => {
          db.close();
          resolve();
        });
        transaction.addEventListener("error", () => reject(new Error("Session setup failed")));
      });
    });
  });
  await page.route("**/auth/worker", (route) => route.fulfill({ status: 204 }));
  await page.route("http://127.0.0.1:4173/auth/session", (route) => route.fulfill({ status: 204 }));
});

function turn(seq: number) {
  return {
    seq: String(seq),
    created_at: "2026-10-01T12:00:00Z",
    messages: [
      {
        ordinal: 0,
        role: "user",
        parts: [{ ordinal: 0, part_type: "text", text: `Thought number ${seq}` }],
      },
      {
        ordinal: 1,
        role: "assistant",
        parts: [
          {
            ordinal: 0,
            part_type: "text",
            text: `An answer to thought ${seq}.\n\nA little more detail to make this a useful conversation.`,
          },
        ],
      },
    ],
  };
}
const emptyHistory: Page = { turns: [], head_seq: "0", has_more: false };

async function backend(
  page: BrowserPage,
  history: Page = emptyHistory,
  onSend?: (send: (value: Frame, damage?: boolean) => void) => void,
  paginate = false,
  sealers = new Map<string, TestSealer>(),
) {
  const requests: ClientFrame[] = [];
  await page.route("**/api/turns?**", (route) => {
    const before = new URL(route.request().url()).searchParams.get("after_seq");
    const response =
      before === "0"
        ? history
        : {
            turns: Array.from({ length: 10 }, (_, i) => turn(10 - i)),
            head_seq: "30",
            has_more: false,
          };
    const available = history.turns.filter(
      (item) => before === "0" || Number(item.seq) < Number(before),
    );
    const limit = Number(new URL(route.request().url()).searchParams.get("limit"));
    const selected = paginate
      ? { ...history, turns: available.slice(0, limit), has_more: available.length > limit }
      : response;
    const sealer = sealers.get(route.request().headers()["q15-channel"] ?? "");
    if (!sealer) throw new Error("Missing content session");
    return route.fulfill({ json: sealer.seal(frame("history", selected, sealer.channelID)) });
  });
  await page.routeWebSocket("**/ws", (socket) => {
    let sealer: TestSealer | undefined;
    const send = (value: Frame, damage = false) => {
      let wire = sealer?.seal(value) ?? value;
      if (damage) {
        const envelope = parseSealed(wire.payload);
        wire = {
          ...wire,
          payload: {
            ...envelope,
            chunks: envelope.chunks.map((chunk, index) =>
              index === 0 ? { ...chunk, data: "AAAA" } : chunk,
            ),
          },
        };
      }
      socket.send(JSON.stringify(wire));
    };
    socket.onMessage((data) => {
      let wire = parseWireFrame(String(data));
      if (sealer) wire = sealer.open(wire);
      const request = parseClientFrame(JSON.stringify(wire));
      requests.push(request);
      if (request.type === "hello") {
        sealer = new TestSealer(request.payload);
        sealers.set(sealer.channelID, sealer);
        send(sealer.key);
        socket.send(
          JSON.stringify(
            frame("ready", {
              head_seq: history.head_seq,
              cursor: history.turns[0]?.seq ?? "0",
              device_id: "test",
            }),
          ),
        );
      }
      if (request.type === "msg.send") {
        const p = request.payload;
        const queued = requests.filter((r) => r.type === "msg.send").length > 1;
        socket.send(
          JSON.stringify(
            frame("msg.status", {
              turn: "31",
              state: queued ? "queued" : "accepted",
              client_msg_id: p.client_msg_id,
              queued,
            }),
          ),
        );
        if (!queued)
          socket.send(
            JSON.stringify(frame("turn.start", { turn: "31", msg: { turn: "31", ordinal: -1 } })),
          );
        if (!queued) onSend?.(send);
      }
      if (request.type === "msg.abort")
        send(
          frame("msg.final", {
            msg: { turn: "31", ordinal: -1 },
            status: "aborted",
            full_text: "Stopped safely.",
          }),
        );
    });
  });
  return requests;
}

test("send, queue, stop, themes, and narrow screens work in a browser", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const requests = await backend(page);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Start a conversation" })).toBeVisible();
  const input = page.getByLabel("Message q15");
  await input.fill("first question");
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await input.fill("second question");
  await page.getByRole("button", { name: "Queue message", exact: true }).click();
  await expect(page.getByText("Queued", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Stop response" }).click();
  await expect(page.getByText("Stopped safely.")).toBeVisible();
  expect(requests.filter((r) => r.type === "msg.send")).toHaveLength(2);
  expect(requests.find((r) => r.type === "msg.abort")?.payload).toEqual({ turn: "31" });
  await page.getByRole("button", { name: /Switch to .* theme/u }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "latte");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByLabel("Open navigation").click();
  await expect(page.getByRole("complementary", { name: "Chat navigation" })).toBeInViewport();
  await page.getByRole("button", { name: "Close navigation", exact: true }).last().click();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  expect(errors).toEqual([]);
});

test("installation only appears for an event with an available prompt", async ({ page }) => {
  await backend(page);
  await page.goto("/");
  const install = page.getByRole("button", { name: "Install", exact: true });
  await page.evaluate(() => window.dispatchEvent(new Event("beforeinstallprompt")));
  await expect(install).toHaveCount(0);
  await page.evaluate(() => {
    const event = new Event("beforeinstallprompt", { cancelable: true });
    Object.defineProperty(event, "prompt", {
      value: () => {
        document.documentElement.dataset.prompted = "true";
      },
    });
    window.dispatchEvent(event);
  });
  await expect(install).toBeVisible();
  await install.click();
  await expect(page.locator("html")).toHaveAttribute("data-prompted", "true");
  await expect(install).toHaveCount(0);
});

test("prepending history preserves the visible message position and deep links page backwards", async ({
  page,
}) => {
  await backend(page, {
    turns: Array.from({ length: 20 }, (_, i) => turn(30 - i)),
    head_seq: "31",
    has_more: true,
  });
  await page.goto("/");
  await expect(page.locator('[data-message-key="30:1"]')).toBeVisible();
  const scroller = page.getByLabel("Conversation", { exact: true });
  await scroller.evaluate((node) => {
    const element = node;
    element.scrollTop = 300;
  });
  const target = page.locator('[data-message-key="12:0"]');
  const before = await target.evaluate((node) => node.getBoundingClientRect().top);
  await page
    .getByRole("button", { name: "Earlier messages" })
    .evaluate((node: HTMLButtonElement) => node.click());
  await expect(page.locator('[data-message-key="1:0"]')).toBeAttached();
  const after = await target.evaluate((node) => node.getBoundingClientRect().top);
  expect(Math.abs(after - before)).toBeLessThan(3);
  await page.goto("/#message-1:0");
  await expect(page.locator('[data-message-key="1:0"]')).toBeInViewport();
});

test("startup loads five complete turns and older pages load ten without moving the reader", async ({
  page,
}) => {
  const queries: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/turns") queries.push(url.search);
  });
  await backend(
    page,
    {
      turns: Array.from({ length: 20 }, (_, index) => turn(30 - index)),
      head_seq: "31",
      has_more: true,
    },
    undefined,
    true,
  );
  await page.goto("/");
  const messages = page.locator("article[data-message-key]");
  await expect(messages).toHaveCount(10);
  expect(queries).toEqual(["?after_seq=0&limit=5"]);
  const anchor = page.locator('[data-message-key="30:1"]');
  const before = await anchor.evaluate((node) => node.getBoundingClientRect().top);
  const older = page.getByRole("button", { name: "Earlier messages" });
  await older.evaluate((node: HTMLButtonElement) => node.click());
  await expect(messages).toHaveCount(30);
  expect(queries.at(-1)).toBe("?after_seq=26&limit=10");
  expect(await anchor.evaluate((node) => node.getBoundingClientRect().top)).toBeCloseTo(before, 0);
  await older.evaluate((node: HTMLButtonElement) => node.click());
  await expect(messages).toHaveCount(40);
  expect(queries.at(-1)).toBe("?after_seq=16&limit=10");
  await expect(older).toBeHidden();
});

test("streaming preserves history anchors, deep links and disclosures through completion and resync", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  const history: Page = {
    turns: Array.from({ length: 20 }, (_, i) => turn(30 - i)),
    head_seq: "31",
    has_more: true,
  };
  let deliver: ((value: Frame) => void) | undefined;
  await backend(page, history, (send) => {
    deliver = send;
  });
  await page.goto("/");
  await page.getByLabel("Message q15").fill("Read while growing");
  await page.getByLabel("Send message", { exact: true }).click();
  const msg = { turn: "31", ordinal: -1 };
  const call = { id: "stable-tool", name: "exec", arguments: '{"command":"pwd"}' };
  required(deliver)(frame("delta", { msg, seq: "1", kind: "tool_call", text: "", call }));
  required(deliver)(
    frame("delta", { msg, seq: "2", kind: "tool_result", text: "/workspace", call }),
  );
  const activity = page.locator("[data-agent-activity]").last();
  const tool = page.locator('[data-tool-call-id="stable-tool"]');
  await activity.locator(":scope > summary").press("Enter");
  await tool.locator(":scope > summary").press("Enter");
  required(deliver)(
    frame("delta", { msg, seq: "3", kind: "text", text: "Growing answer.\n\n".repeat(20) }),
  );
  const answer = page.locator('article[data-message-key="31:-1"]');
  await expect(answer).toContainText("Growing answer.");
  const scroller = page.getByLabel("Conversation", { exact: true });
  await scroller.evaluate((node) => {
    node.scrollTop = 300;
  });
  await expect(page.getByRole("button", { name: "Back to latest" })).toBeVisible();
  const anchor = page.locator('[data-message-key="12:0"]');
  const before = await anchor.evaluate((node) => node.getBoundingClientRect().top);
  required(deliver)(
    frame("delta", { msg, seq: "4", kind: "text", text: "More live detail.\n\n".repeat(20) }),
  );
  await page
    .getByRole("button", { name: "Earlier messages" })
    .evaluate((node: HTMLButtonElement) => node.click());
  await expect(page.locator('[data-message-key="1:0"]')).toBeAttached();
  expect(await anchor.evaluate((node) => node.getBoundingClientRect().top)).toBeCloseTo(before, 0);
  await page.evaluate(() => {
    location.hash = "message-1:0";
  });
  await expect(page.locator('[data-message-key="1:0"]')).toBeInViewport();
  const position = await scroller.evaluate((node) => node.scrollTop);
  required(deliver)(
    frame("snapshot", { msg, seq: "5", kind: "text", text: "Replacement answer.\n\n".repeat(40) }),
  );
  await expect(answer).toContainText("Replacement answer.");
  expect(await scroller.evaluate((node) => node.scrollTop)).toBeCloseTo(position, 0);
  history.turns.unshift({
    seq: "31",
    created_at: "2026-10-01T12:00:00Z",
    messages: [
      {
        ordinal: 0,
        role: "assistant",
        parts: [
          { ordinal: 0, part_type: "tool_call", tool_call: call },
          { ordinal: 1, part_type: "tool_result", tool_call_id: call.id, content: "/workspace" },
          { ordinal: 2, part_type: "text", disposition: "final", text: "Final canonical answer." },
        ],
      },
    ],
  });
  required(deliver)(
    frame("msg.final", { msg, status: "completed", full_text: "Final canonical answer." }),
  );
  await expect(page.locator('article[data-message-key="31:0"]')).toContainText(
    "Final canonical answer.",
  );
  await expect(activity).toHaveAttribute("open", "");
  await expect(tool).toHaveAttribute("open", "");
  expect(await scroller.evaluate((node) => node.scrollTop)).toBeCloseTo(position, 0);
  required(deliver)(frame("error", { code: "resync_from_head", ref: "" }));
  await expect(page.getByText("Chat reconnected. Recent history refreshed.")).toBeVisible();
  await expect(activity).toHaveAttribute("open", "");
  await expect(tool).toHaveAttribute("open", "");
  expect(await scroller.evaluate((node) => node.scrollTop)).toBeCloseTo(position, 0);
  await page.getByRole("button", { name: "Back to latest" }).click();
  await expect
    .poll(() => scroller.evaluate((node) => node.scrollHeight - node.scrollTop - node.clientHeight))
    .toBeLessThan(2);
});

test("generated precache contains only shell files and preserves the embed sentinel", () => {
  const root = new URL("../../internal/assets/dist/", import.meta.url);
  const manifest = parseShellManifest(
    JSON.parse(readFileSync(new URL("shell-manifest.json", root), "utf8")),
  );
  expect(manifest.version).toMatch(/^[a-f0-9]{16}$/u);
  expect(manifest.paths).toContain("/index.html");
  expect(manifest.paths).toContain("/manifest.webmanifest");
  expect(
    manifest.paths.every(
      (path) =>
        [
          "/index.html",
          "/manifest.webmanifest",
          "/icon.svg",
          "/icon-192.png",
          "/icon-512.png",
        ].includes(path) || path.startsWith("/assets/"),
    ),
  ).toBe(true);
  const digest = createHash("sha256");
  for (const path of manifest.paths) {
    digest.update(path);
    digest.update(readFileSync(new URL(path.slice(1), root)));
  }
  expect(manifest.version).toBe(digest.digest("hex").slice(0, 16));
  expect(readFileSync(new URL(".gitkeep", root), "utf8")).toBe("");
});

test("the compiled worker loads the shell offline without caching chat data", async ({
  page,
  context,
}) => {
  const history = turn(30);
  required(required(history.messages[0]).parts[0]).text =
    "Private history stays out of the shell cache";
  await backend(page, { turns: [history], head_seq: "30", has_more: false });
  await page.goto("/");
  await expect(
    page
      .getByLabel("Conversation", { exact: true })
      .getByText("Private history stays out of the shell cache", { exact: true }),
  ).toBeVisible();
  const manifest = parseShellManifest(
    await (await page.request.get("/shell-manifest.json")).json(),
  );
  await page.evaluate(() => navigator.serviceWorker.ready);
  await page.reload();
  await expect
    .poll(() => page.evaluate(() => navigator.serviceWorker.controller?.scriptURL))
    .toMatch(/\/sw\.js$/u);
  const cached = await page.evaluate(async () => {
    const names = await caches.keys();
    const entries = await Promise.all(names.map(async (name) => (await caches.open(name)).keys()));
    return {
      names,
      paths: entries
        .flat()
        .map((request) => new URL(request.url).pathname)
        .toSorted(),
    };
  });
  expect(cached.names).toEqual([`q15-shell-${manifest.version}`]);
  expect(cached.paths).toEqual(manifest.paths);

  await page.unroute("**/api/turns?**");
  await context.setOffline(true);
  await page.reload({ waitUntil: "domcontentloaded" });
  await expect(page.getByLabel("Message q15")).toBeVisible();
  await expect(
    page
      .getByLabel("Conversation", { exact: true })
      .getByText("Private history stays out of the shell cache", { exact: true }),
  ).toBeHidden();
  expect(
    await page.evaluate(() =>
      fetch("/api/turns?after_seq=0&limit=1").then(
        (response) => response.status,
        () => null,
      ),
    ),
  ).toBeNull();
});

test("a message link in the initial page waits for history to load", async ({ page }) => {
  await backend(page, {
    turns: Array.from({ length: 20 }, (_, i) => turn(30 - i)),
    head_seq: "31",
    has_more: false,
  });
  await page.goto("/#message-30:1");
  await expect(page.locator('[data-message-key="30:1"]')).toBeInViewport();
});

function toolHistory(): Page {
  return {
    head_seq: "71",
    has_more: false,
    turns: [
      {
        seq: "70",
        created_at: "2026-10-01T12:00:00Z",
        messages: [
          {
            ordinal: 0,
            role: "user",
            parts: [
              { ordinal: 0, part_type: "text", text: "Check the workspace and search the web." },
            ],
          },
          {
            ordinal: 1,
            role: "assistant",
            parts: [
              {
                ordinal: 0,
                part_type: "text",
                disposition: "commentary",
                text: "I’ll check both.",
              },
              {
                ordinal: 1,
                part_type: "tool_call",
                tool_call: { id: "command", name: "exec", arguments: '{"command":"pwd"}' },
              },
            ],
          },
          {
            ordinal: 2,
            role: "tool",
            parts: [
              {
                ordinal: 0,
                part_type: "tool_result",
                tool_call_id: "command",
                content: "/workspace",
              },
            ],
          },
          {
            ordinal: 3,
            role: "assistant",
            parts: [
              {
                ordinal: 0,
                part_type: "tool_call",
                tool_call: {
                  id: "search",
                  name: "web_search",
                  arguments:
                    '{"query":"a long search query that should truncate gracefully on small screens without overflowing the conversation"}',
                },
              },
            ],
          },
          {
            ordinal: 4,
            role: "tool",
            parts: [
              {
                ordinal: 0,
                part_type: "tool_result",
                tool_call_id: "search",
                content: "Search unavailable",
                is_error: true,
              },
            ],
          },
          {
            ordinal: 5,
            role: "assistant",
            parts: [
              {
                ordinal: 0,
                part_type: "text",
                disposition: "final",
                text: "The workspace is **ready**. The search failed.",
              },
            ],
          },
        ],
      },
    ],
  };
}

test("agent turns own their identity, answer and compact expandable tool work", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await backend(page, toolHistory());
  await page.goto("/");
  const activity = page.locator("[data-agent-activity]");
  const agent = page.locator("[data-agent-turn]");
  const identity = agent.locator("[data-turn-identity]");
  await expect(identity).toHaveCount(1);
  await expect(identity.getByText("q15", { exact: true })).toBeVisible();
  await expect(identity.locator("svg")).toBeVisible();
  await expect(agent.locator("[data-agent-activity]")).toHaveCount(1);
  expect(await agent.evaluate((node) => [...node.children].map((child) => child.tagName))).toEqual([
    "DIV",
    "ARTICLE",
    "DETAILS",
  ]);
  await expect(page.getByText("Used 2 tools")).toBeVisible();
  await expect(page.getByText("ready", { exact: true })).toBeVisible();
  await expect(page.getByLabel("Copy response")).toHaveCount(1);
  await expect(page.getByText("/workspace", { exact: true })).toBeHidden();
  const spacing = await agent.evaluate((node) => {
    const user = node.previousElementSibling;
    const answer = node.querySelector("article");
    const work = node.querySelector("[data-agent-activity]");
    if (!user || !answer || !work) throw new Error("Expected a complete exchange.");
    return {
      betweenTurns: node.getBoundingClientRect().top - user.getBoundingClientRect().bottom,
      insideTurn: work.getBoundingClientRect().top - answer.getBoundingClientRect().bottom,
    };
  });
  expect(spacing.insideTurn).toBeGreaterThan(0);
  expect(spacing.betweenTurns).toBeGreaterThan(spacing.insideTurn);
  await page.screenshot({ path: "test-results/activity-collapsed.png" });
  await page.getByText("Used 2 tools").click();
  await expect(page.getByText("I’ll check both.")).toBeVisible();
  await expect(activity.locator("[data-tool-call-id]")).toHaveCount(2);
  await page.getByText("Ran command", { exact: true }).click();
  await expect(page.getByText("/workspace", { exact: true })).toBeVisible();
  await page.getByText("Searched the web", { exact: true }).click();
  await expect(page.getByText("Search unavailable", { exact: true })).toBeVisible();
  await expect(page.getByText("1 error", { exact: true })).toBeVisible();
  for (let ordinal = 0; ordinal < 6; ordinal++)
    await expect(page.locator(`[id="message-70:${ordinal}"]`)).toHaveCount(1);
  await page.screenshot({ path: "test-results/activity-desktop.png" });
  await page.getByRole("button", { name: /Switch to .* theme/u }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "latte");
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole("complementary", { name: "Chat navigation" })).not.toBeInViewport();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: "test-results/activity-mobile.png" });
});

test("deep links reveal completed tool outputs inside both disclosures", async ({ page }) => {
  await backend(page, toolHistory());
  await page.goto("/#message-70:2");
  await expect(page.locator('[id="message-70:2"]')).toBeInViewport();
  await expect(page.getByText("/workspace", { exact: true })).toBeVisible();
  await expect(page.locator("[data-agent-activity]")).toHaveAttribute("open", "");
  await expect(page.locator('[data-tool-call-id="command"]')).toHaveAttribute("open", "");
});

test("live work stays closed by default and tool updates preserve the reader's choice", async ({
  page,
}) => {
  let deliver: ((value: Frame) => void) | undefined;
  await backend(page, undefined, (send) => {
    deliver = send;
  });
  await page.goto("/");
  await page.getByLabel("Message q15").fill("Check the workspace");
  await page.getByLabel("Send message", { exact: true }).click();
  await expect(page.getByLabel("Stop response")).toBeVisible();
  const agent = page.locator("[data-agent-turn]");
  const identity = agent.locator("[data-turn-identity]");
  await expect(identity.getByText("q15", { exact: true })).toBeVisible();
  await expect(identity.locator("svg")).toBeVisible();
  await expect(agent.locator("article")).toHaveCount(0);
  const call = { id: "live-command", name: "exec", arguments: '{"command":"pwd"}' };
  required(deliver)(
    frame("snapshot", {
      msg: { turn: "31", ordinal: -1 },
      kind: "model_start",
      text: "",
      seq: "0",
      model_ref: "model-a",
    }),
  );
  const activity = page.locator("[data-agent-activity]");
  await expect(page.getByText("Thinking…", { exact: true })).toBeVisible();
  await expect(activity).not.toHaveAttribute("open");
  await expect(identity.getByText("model-a", { exact: true })).toBeVisible();
  await expect(identity.getByRole("link", { name: "Link to message 31:-1" })).toBeVisible();
  required(deliver)(
    frame("delta", {
      msg: { turn: "31", ordinal: -1 },
      kind: "reasoning",
      text: "Considering the workspace.",
      seq: "1",
    }),
  );
  await expect(agent.locator("article")).toHaveCount(0);
  await expect(agent.locator("[data-turn-identity]")).toHaveCount(1);
  required(deliver)(
    frame("delta", {
      msg: { turn: "31", ordinal: -1 },
      kind: "tool_call",
      text: "",
      call,
      seq: "2",
    }),
  );
  await expect(page.getByText("Using tools…", { exact: true })).toBeVisible();
  await expect(activity).not.toHaveAttribute("open");
  await expect(page.getByText("Running command", { exact: true })).toBeHidden();
  await activity.locator(":scope > summary").press("Enter");
  await expect(page.getByText("Running command", { exact: true })).toBeVisible();
  const promptTop = await page
    .getByText("Check the workspace", { exact: true })
    .evaluate((node) => node.getBoundingClientRect().top);
  const workTop = await page
    .locator("[data-agent-activity]")
    .evaluate((node) => node.getBoundingClientRect().top);
  expect(promptTop).toBeLessThan(workTop);
  await expect(page.locator("[data-agent-activity]")).toHaveAttribute("open", "");
  required(deliver)(
    frame("delta", {
      msg: { turn: "31", ordinal: -1 },
      kind: "tool_result",
      text: "/workspace",
      call,
      seq: "3",
      is_error: false,
    }),
  );
  await expect(page.getByText("Completed", { exact: true })).toBeVisible();
  await expect(activity).toHaveAttribute("open", "");
  required(deliver)(
    frame("snapshot", {
      msg: { turn: "31", ordinal: -1 },
      kind: "model_start",
      text: "",
      loop_turn: 1,
      seq: "4",
    }),
  );
  await expect(page.getByText("Completed", { exact: true })).toBeVisible();
  await activity.locator(":scope > summary").press("Enter");
  required(deliver)(
    frame("msg.final", {
      msg: { turn: "31", ordinal: -1 },
      status: "completed",
      full_text: "The workspace is ready.",
    }),
  );
  await expect(page.getByText("The workspace is ready.", { exact: true })).toBeVisible();
  await expect(page.locator("[data-agent-activity]")).not.toHaveAttribute("open");
  await expect(page.getByLabel("Copy response")).toHaveCount(1);
  await expect(agent.locator("[data-turn-identity]")).toHaveCount(1);
  expect(await agent.evaluate((node) => [...node.children].map((child) => child.tagName))).toEqual([
    "DIV",
    "ARTICLE",
    "DETAILS",
  ]);
});

test("enlarged text preserves conversation space, navigation, and keyboard access", async ({
  page,
}) => {
  await backend(page, toolHistory());
  await page.goto("/");
  const answer = page.locator('[id="message-70:5"] p');
  await expect(answer).toBeVisible();
  const originalSize = await answer.evaluate((node) =>
    Number.parseFloat(getComputedStyle(node).fontSize),
  );
  await page.evaluate(() => {
    document.documentElement.style.fontSize = "200%";
  });
  expect(
    await answer.evaluate((node) => Number.parseFloat(getComputedStyle(node).fontSize)),
  ).toBeCloseTo(originalSize * 2);
  await expect(page.getByLabel("Open navigation")).toBeVisible();
  await expect(page.getByRole("complementary", { name: "Chat navigation" })).toBeHidden();
  const transcript = page.getByLabel("Conversation", { exact: true });
  expect(await transcript.evaluate((node) => node.clientHeight / innerHeight)).toBeGreaterThan(0.4);
  const skip = page.getByRole("link", { name: "Skip to message input" });
  await expect(skip).not.toBeInViewport();
  await skip.focus();
  await expect(skip).toBeInViewport();
  await skip.press("Enter");
  await expect(page.getByLabel("Message q15")).toBeFocused();
  await page.setViewportSize({ width: 320, height: 844 });
  await page.getByLabel("Open navigation").click();
  const navigation = page.getByRole("complementary", { name: "Chat navigation" });
  await expect(navigation).toBeInViewport();
  expect(await navigation.evaluate((node) => node.scrollWidth <= node.clientWidth)).toBe(true);
  await expect(navigation.getByRole("link", { name: "Chat", exact: true })).toBeInViewport();
  await navigation.getByRole("button", { name: "Close navigation", exact: true }).click();
  await expect(navigation).toBeHidden();
  await page.getByText("Used 2 tools").click();
  await page.getByText("Ran command", { exact: true }).click();
  await expect(page.getByText("/workspace", { exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test("short and narrow screens keep long drafts and send controls usable", async ({ page }) => {
  const requests = await backend(page, toolHistory());
  await page.goto("/");
  const input = page.getByLabel("Message q15");
  const scroller = page.getByLabel("Conversation", { exact: true });
  const draft = "A longer thought that needs more than one line. ".repeat(20);
  for (const viewport of [
    { width: 1280, height: 600 },
    { width: 640, height: 450 },
    { width: 320, height: 640 },
  ]) {
    await page.setViewportSize(viewport);
    await input.fill(draft);
    expect(await scroller.evaluate((node) => node.clientHeight / innerHeight)).toBeGreaterThan(
      0.25,
    );
    const form = await page.locator("form").boundingBox();
    expect(required(form).y + required(form).height).toBeLessThanOrEqual(viewport.height);
    expect(await input.evaluate((node) => node.scrollHeight > node.clientHeight)).toBe(true);
    await expect(page.getByLabel("Send message", { exact: true })).toBeInViewport();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
  }
  await page.getByLabel("Send message", { exact: true }).click();
  expect(requests.find((request) => request.type === "msg.send")?.payload).toMatchObject({
    text: draft.trim(),
  });
  await input.fill("The next thought");
  await expect(page.getByLabel("Stop response")).toBeInViewport();
  await expect(page.getByLabel("Queue message", { exact: true })).toBeInViewport();
  await page.getByLabel("Queue message", { exact: true }).click();
  await page.getByLabel("Stop response").click();
  await expect(page.getByText("Stopped safely.")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test("motion follows a changed accessibility preference and themes have a native fallback", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await backend(page);
  await page.goto("/");
  const heading = page.getByRole("heading", { name: "Start a conversation" });
  await expect(heading).toBeVisible();
  const afterRapidToggle = await page.getByRole("button", { name: /Switch to .* theme/u }).evaluate(
    (button: HTMLButtonElement) =>
      new Promise<string | undefined>((resolve) => {
        let changes = 0;
        const observer = new MutationObserver((records) => {
          changes += records.length;
          if (changes >= 2) {
            observer.disconnect();
            resolve(document.documentElement.dataset.theme);
          }
        });
        observer.observe(document.documentElement, {
          attributes: true,
          attributeFilter: ["data-theme"],
        });
        button.click();
        button.click();
      }),
  );
  expect(afterRapidToggle).toBe("mocha");
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          document.getAnimations().filter((a) => a.effect?.getTiming().iterations === Infinity)
            .length,
      ),
    )
    .toBeGreaterThan(0);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect.poll(() => page.evaluate(() => document.getAnimations().length)).toBe(0);
  // Reduced motion must bypass the snapshot effect, including on theme changes.
  await page.evaluate(() =>
    Object.defineProperty(document, "startViewTransition", {
      configurable: true,
      value: () => {
        throw new Error("Reduced motion must not create a view transition");
      },
    }),
  );
  await page.getByRole("button", { name: /Switch to .* theme/u }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "latte");
  await expect.poll(() => page.evaluate(() => document.getAnimations().length)).toBe(0);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByLabel("Open navigation").click();
  await expect(page.getByRole("complementary")).toBeInViewport();
  await page
    .getByRole("complementary")
    .getByRole("button", { name: "Close navigation", exact: true })
    .click();
  await expect(page.getByRole("complementary")).toBeHidden();
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          document.getAnimations().filter((a) => a.effect?.getTiming().iterations === Infinity)
            .length,
      ),
    )
    .toBeGreaterThan(0);
  await page.evaluate(() =>
    Object.defineProperty(document, "startViewTransition", { value: undefined }),
  );
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.getByRole("button", { name: /Switch to .* theme/u }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "mocha");
  await expect(heading).toBeVisible();
  expect(errors).toEqual([]);
});

test("animated tool disclosures follow the latest message without moving a reader in history", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  let deliver: ((value: Frame) => void) | undefined;
  await backend(
    page,
    {
      turns: Array.from({ length: 20 }, (_, i) => turn(30 - i)),
      head_seq: "31",
      has_more: false,
    },
    (send) => {
      deliver = send;
    },
  );
  await page.goto("/");
  const scroller = page.getByLabel("Conversation", { exact: true });
  const gap = () =>
    scroller.evaluate((node) => node.scrollHeight - node.scrollTop - node.clientHeight);
  await expect.poll(gap).toBeLessThan(2);
  // Reproduce a queued scroll notification reaching a newly enlarged layout
  // before ResizeObserver follows it. The reader has not moved their position.
  await scroller.evaluate((node) => {
    document.documentElement.style.fontSize = "105%";
    node.dispatchEvent(new Event("scroll", { bubbles: true }));
  });
  await expect.poll(gap).toBeLessThan(2);
  await expect(page.getByRole("button", { name: "Back to latest" })).toBeHidden();
  await page.getByLabel("Message q15").fill("Read the workspace");
  await page.getByLabel("Send message", { exact: true }).click();
  const msg = { turn: "31", ordinal: -1 };
  const call = { id: "motion-command", name: "exec", arguments: '{"command":"ls"}' };
  required(deliver)(frame("snapshot", { msg, kind: "model_start", text: "", seq: "0" }));
  required(deliver)(frame("delta", { msg, kind: "tool_call", text: "", call, seq: "1" }));
  await page.locator("[data-agent-activity] > summary").click();
  const summary = page.locator('[data-tool-call-id="motion-command"] > summary');
  await expect(summary).toBeVisible();
  await summary.focus();
  await summary.press("Enter");
  await expect(page.getByText("Waiting for the tool result…")).toBeVisible();
  required(deliver)(
    frame("delta", {
      msg,
      kind: "tool_result",
      text: Array.from({ length: 60 }, (_, i) => `File ${i}`).join("\n"),
      call,
      seq: "2",
    }),
  );
  await expect(page.getByText("Completed", { exact: true })).toBeVisible();
  await expect.poll(gap).toBeLessThan(2);
  await scroller.evaluate((node) => {
    const element = node;
    element.scrollTop -= 400;
  });
  await expect(page.getByRole("button", { name: "Back to latest" })).toBeVisible();
  const position = await scroller.evaluate((node) => node.scrollTop);
  required(deliver)(
    frame("snapshot", { msg, kind: "model_start", text: "", seq: "3", loop_turn: 1 }),
  );
  required(deliver)(
    frame("delta", {
      msg,
      kind: "text",
      text: "More detail about the workspace.\n\n".repeat(20),
      seq: "4",
    }),
  );
  await expect(page.getByText(/More detail about the workspace/u).first()).toBeAttached();
  expect(await scroller.evaluate((node) => node.scrollTop)).toBeCloseTo(position, 0);
  await page.getByRole("button", { name: "Back to latest" }).click();
  await expect.poll(gap).toBeLessThan(2);
  await summary.evaluate((node: HTMLElement) => node.click());
  await expect(page.locator('[data-tool-call-id="motion-command"] pre').last()).toBeHidden();
  await expect.poll(gap).toBeLessThan(2);
});

test("working glow and font axes animate without moving text, and settle with reduced motion", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  let deliver: ((value: Frame) => void) | undefined;
  await backend(page, undefined, (send) => {
    deliver = send;
  });
  await page.goto("/");
  await page.getByLabel("Message q15").fill("Think through a useful next step");
  await page.evaluate(() => document.fonts.ready);
  await page.getByLabel("Send message", { exact: true }).click();
  const activity = page.locator("[data-agent-activity]");
  const summary = activity.locator(":scope > summary");
  const label = summary.getByText("Thinking…", { exact: true });
  const floating = label.locator("..");
  await expect(activity).not.toHaveAttribute("open");
  await expect(activity).toHaveAttribute("data-phase", "thinking");
  const before = await label.evaluate((node) => ({
    axes: getComputedStyle(node).fontVariationSettings,
    width: node.getBoundingClientRect().width,
  }));
  await expect
    .poll(() => label.evaluate((node) => getComputedStyle(node).fontVariationSettings))
    .not.toBe(before.axes);
  expect(await label.evaluate((node) => node.getBoundingClientRect().width)).toBeCloseTo(
    before.width,
    1,
  );
  await expect
    .poll(() => floating.evaluate((node) => getComputedStyle(node).transform))
    .not.toBe("none");
  expect(
    await summary.evaluate((node) => Number(getComputedStyle(node, "::before").opacity)),
  ).toBeGreaterThan(0);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect.poll(() => page.evaluate(() => document.getAnimations().length)).toBe(0);
  await expect
    .poll(() => floating.evaluate((node) => getComputedStyle(node).transform))
    .toBe("none");
  const stationary = await label.evaluate(async (node) => {
    const samples: string[] = [];
    for (let i = 0; i < 6; i++) {
      await new Promise<void>((resolve) => {
        requestAnimationFrame(() => {
          resolve();
        });
      });
      samples.push(getComputedStyle(node).fontVariationSettings);
    }
    return samples;
  });
  expect(new Set(stationary).size).toBe(1);
  const msg = { turn: "31", ordinal: -1 };
  const call = { id: "glow-command", name: "exec", arguments: '{"command":"pwd"}' };
  required(deliver)(frame("delta", { msg, kind: "tool_call", text: "", call, seq: "1" }));
  await expect(activity).toHaveAttribute("data-phase", "tool");
  await expect(activity).not.toHaveAttribute("open");
  await expect(page.getByText("Using tools…", { exact: true })).toBeVisible();
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          document.getAnimations().filter((a) => a.effect?.getTiming().iterations === Infinity)
            .length,
      ),
    )
    .toBeGreaterThan(0);
  await summary.press("Enter");
  await expect(page.getByText("Running command", { exact: true })).toBeVisible();
  required(deliver)(
    frame("msg.final", { msg, status: "completed", full_text: "The workspace is ready." }),
  );
  await expect(page.getByText("The workspace is ready.", { exact: true })).toBeVisible();
  await expect(activity).toHaveAttribute("open", "");
  await expect(activity).not.toHaveAttribute("data-working");
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          document.getAnimations().filter((a) => a.effect?.getTiming().iterations === Infinity)
            .length,
      ),
    )
    .toBe(0);
});

test("a damaged sealed frame is visible while later content uses the same socket", async ({
  page,
}) => {
  const recoveredText = `${"Recovered sealed response ".repeat(1500)}done`;
  const sentText = "check corruption ".repeat(2500).trim();
  const requests = await backend(page, emptyHistory, (send) => {
    const msg = { turn: "31", ordinal: -1 };
    send(frame("delta", { msg, seq: "1", kind: "text", text: "cannot be read" }), true);
    send(frame("snapshot", { msg, seq: "2", kind: "text", text: recoveredText }));
    send(frame("msg.final", { msg, status: "aborted", full_text: recoveredText }));
  });
  await page.goto("/");
  await page.getByLabel("Message q15").fill(sentText);
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await expect(
    page.getByText("This content could not be decrypted. Reconnect to try again.", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText(recoveredText, { exact: true })).toBeVisible();
  expect(requests.find((request) => request.type === "msg.send")?.payload).toMatchObject({
    text: sentText,
  });
  await page.getByLabel("Message q15").fill("still connected");
  await expect(page.getByRole("button", { name: "Send message", exact: true })).toBeEnabled();
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await expect.poll(() => requests.filter((request) => request.type === "msg.send").length).toBe(2);
  expect(requests.filter((request) => request.type === "hello")).toHaveLength(1);
});

test("held attachments fit above the message in both palettes and on narrow screens", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  const requests = await backend(page);
  await page.goto("/");
  await expect(page.getByRole("button", { name: "Attach files" })).toBeEnabled();
  const pdfName = "2025_komprimierte_druckvorschau_5899504.pdf";
  await page.getByLabel("Choose attachments").setInputFiles([
    { name: pdfName, mimeType: "application/pdf", buffer: Buffer.alloc(1536 * 1024) },
    { name: "holiday-photo.png", mimeType: "image/png", buffer: Buffer.alloc(96 * 1024) },
    { name: "voice-note.ogg", mimeType: "audio/ogg", buffer: Buffer.alloc(24 * 1024) },
  ]);
  const tray = page.getByRole("region", { name: "Selected attachments" });
  await expect(tray.getByText(pdfName)).toBeVisible();
  await expect(tray.getByText("PDF · 1.5 MiB")).toBeVisible();
  await expect(page.getByLabel(`Remove ${pdfName}`)).toBeEnabled();
  await page.locator("form").screenshot({ path: "test-results/attachments-mocha.png" });
  await page.getByRole("button", { name: /Switch to .* theme/u }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "latte");
  await page.locator("form").screenshot({ path: "test-results/attachments-latte.png" });
  await page.setViewportSize({ width: 320, height: 640 });
  await expect(page.getByLabel("Message q15")).toBeInViewport();
  await expect(page.getByRole("button", { name: "Send message", exact: true })).toBeInViewport();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.locator("form").screenshot({ path: "test-results/attachments-mobile.png" });
  await page.getByLabel(`Remove ${pdfName}`).click();
  await expect(tray.getByText(pdfName)).toHaveCount(0);
  await page.getByLabel("Choose attachments").setInputFiles(
    Array.from({ length: 14 }, (_, index) => ({
      name: `a-very-long-file-name-to-check-composer-overflow-${index}.txt`,
      mimeType: "text/plain",
      buffer: Buffer.alloc(1),
    })),
  );
  await expect(page.getByLabel("Message q15")).toBeInViewport();
  await expect(page.getByRole("button", { name: "Send message", exact: true })).toBeInViewport();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  expect(requests.filter((request) => request.type === "msg.send")).toHaveLength(0);
});

test("uploads on send, renders a resized image and preserves sealed media across reload", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  const sealers = new Map<string, TestSealer>();
  const history: Page = { turns: [], head_seq: "0", has_more: false };
  const files = new Map<string, { filename: string; contentType: string; data: Buffer }>();
  let uploads = 0;
  let parts: Attachment[] = [];
  let completeResponse: (() => void) | undefined;
  await page.route("**/api/media", async (route) => {
    uploads++;
    const headers = route.request().headers();
    expect(headers["q15-proof"]).toBeDefined();
    const sealer = required(sealers.get(headers["q15-channel"] ?? ""));
    const wire = parseWireFrame(required(route.request().postData()));
    expect(JSON.stringify(wire)).not.toContain("picked.png");
    const decoded = sealer.openBytes(wire);
    expect(decoded.contentType).toBe("application/vnd.q15.media");
    const headerLength = decoded.bytes.readUInt32BE(0);
    const descriptors: unknown = JSON.parse(decoded.bytes.subarray(4, 4 + headerLength).toString());
    const metadata = parseMediaFiles(descriptors);
    let offset = 4 + headerLength;
    parts = metadata.map((file) => {
      const data = decoded.bytes.subarray(offset, offset + file.size);
      offset += file.size;
      const ref = "media://sha256/" + createHash("sha256").update(data).digest("hex");
      files.set(ref, { filename: file.filename, contentType: file.content_type, data });
      return {
        part_type: "media",
        media_kind: file.content_type === "image/png" ? "image" : "document",
        media_ref: ref,
        filename: file.filename,
        content_type: file.content_type,
      };
    });
    await route.fulfill({ json: sealer.seal(frame("media.done", { parts }, wire.id)) });
  });
  await page.route("**/api/media/*", async (route) => {
    const sealer = required(sealers.get(route.request().headers()["q15-channel"] ?? ""));
    const ref = "media://sha256/" + new URL(route.request().url()).pathname.split("/").at(-1);
    const file = required(files.get(ref));
    const header = Buffer.from(
      JSON.stringify([
        { filename: file.filename, content_type: file.contentType, size: file.data.length },
      ]),
    );
    const prefix = Buffer.alloc(4);
    prefix.writeUInt32BE(header.length);
    await route.fulfill({
      json: sealer.seal(
        frame("media.get", {}, ref),
        "application/vnd.q15.media",
        Buffer.concat([prefix, header, file.data]),
      ),
    });
  });
  const requests = await backend(
    page,
    history,
    (send) => {
      send(
        frame("delta", {
          msg: { turn: "31", ordinal: -1 },
          seq: "1",
          kind: "text",
          text: "Reading your attachment…",
        }),
      );
      completeResponse = () => {
        history.head_seq = "31";
        history.turns = [
          {
            seq: "31",
            created_at: "2026-10-08T00:00:00Z",
            messages: [
              {
                ordinal: 0,
                role: "user",
                parts: parts.map((part, ordinal) => ({
                  ordinal,
                  part_type: "media",
                  media_kind: part.media_kind,
                  media_ref: part.media_ref,
                })),
              },
            ],
          },
        ];
        send(
          frame("msg.final", {
            msg: { turn: "31", ordinal: -1 },
            status: "completed",
            full_text: "Attachment received.",
          }),
        );
      };
    },
    false,
    sealers,
  );
  await page.goto("/");
  await expect(page.getByRole("button", { name: "Attach files" })).toBeEnabled();
  const encoded = await page.evaluate(() => {
    const canvas = document.createElement("canvas");
    canvas.width = 4096;
    canvas.height = 2048;
    const context = canvas.getContext("2d");
    if (!context) throw new Error("Canvas unavailable");
    context.fillStyle = "#a6e3a1";
    context.fillRect(0, 0, canvas.width, canvas.height);
    return canvas.toDataURL("image/png").split(",")[1];
  });
  await page.getByLabel("Choose attachments").setInputFiles([
    {
      name: "picked.png",
      mimeType: "image/png",
      buffer: Buffer.from(required(encoded), "base64"),
    },
    { name: "report.pdf", mimeType: "application/pdf", buffer: Buffer.from("%PDF-1.7\nreport\n") },
  ]);
  expect(uploads).toBe(0);
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await expect(page.getByText("Reading your attachment…", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Stop response" })).toBeVisible();
  const image = page.getByRole("img", { name: "picked.png" });
  await expect(image).toBeVisible();
  expect(
    await image.evaluate((element) =>
      element instanceof HTMLImageElement ? element.naturalWidth : 0,
    ),
  ).toBe(2048);
  expect(requests.find((request) => request.type === "msg.send")?.payload).toMatchObject({
    text: "",
    parts,
  });
  expect(uploads).toBe(1);
  await expect(page.getByText("report.pdf", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Download", exact: true })).toHaveCount(2);
  await page.screenshot({ path: "test-results/attachments-streaming-mocha.png" });
  await page.getByRole("button", { name: /Switch to .* theme/u }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "latte");
  await page.screenshot({ path: "test-results/attachments-streaming-latte.png" });
  await page.setViewportSize({ width: 320, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await expect(page.getByRole("button", { name: "Stop response" })).toBeInViewport();
  await page.screenshot({ path: "test-results/attachments-streaming-mobile.png" });
  required(completeResponse)();
  await expect(page.getByRole("button", { name: "Stop response" })).toHaveCount(0);
  await expect(image).toBeVisible();
  await page.reload();
  await expect(page.getByRole("img", { name: "picked.png" })).toBeVisible();
  await expect(page.getByText("report.pdf", { exact: true })).toBeVisible();
  expect(uploads).toBe(1);
});
