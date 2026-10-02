import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { expect, test } from "@playwright/test";
import type { Page as BrowserPage } from "@playwright/test";
import type { Frame, Page } from "../src/generated/protocol";
import { frame } from "../src/protocol";

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
async function backend(
  page: BrowserPage,
  history: Page = { turns: [], head_seq: "0", has_more: false },
  onSend?: (send: (value: Frame) => void) => void,
) {
  const requests: Frame[] = [];
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
    return route.fulfill({ json: response });
  });
  await page.routeWebSocket("**/ws", (socket) => {
    socket.onMessage((data) => {
      const request = JSON.parse(String(data)) as Frame;
      requests.push(request);
      if (request.type === "hello")
        socket.send(
          JSON.stringify(
            frame("ready", {
              head_seq: history.head_seq,
              cursor: history.turns[0]?.seq ?? "0",
              device_id: "test",
            }),
          ),
        );
      if (request.type === "msg.send") {
        const p = request.payload as { client_msg_id: string };
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
        if (!queued) onSend?.((value) => socket.send(JSON.stringify(value)));
      }
      if (request.type === "msg.abort")
        socket.send(
          JSON.stringify(
            frame("msg.final", {
              msg: { turn: "31", ordinal: -1 },
              status: "aborted",
              full_text: "Stopped safely.",
            }),
          ),
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
  await page.getByRole("button", { name: /Switch to .* theme/ }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "latte");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByLabel("Open navigation").click();
  await expect(page.getByRole("complementary", { name: "Chat navigation" })).toBeInViewport();
  await page.getByRole("button", { name: "Close navigation", exact: true }).last().click();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  expect(errors).toEqual([]);
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
    node.scrollTop = 300;
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

test("generated precache contains only shell files and preserves the embed sentinel", () => {
  const root = new URL("../../internal/assets/dist/", import.meta.url);
  const manifest = JSON.parse(readFileSync(new URL("shell-manifest.json", root), "utf8")) as {
    version: string;
    paths: string[];
  };
  expect(manifest.version).toMatch(/^[a-f0-9]{16}$/);
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
        ].includes(path) || /^\/assets\//.test(path),
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
  history.messages[0]!.parts[0]!.text = "Private history stays out of the shell cache";
  await backend(page, { turns: [history], head_seq: "30", has_more: false });
  await page.goto("/");
  await expect(
    page
      .getByLabel("Conversation", { exact: true })
      .getByText("Private history stays out of the shell cache", { exact: true }),
  ).toBeVisible();
  const manifest: { version: string; paths: string[] } = await (
    await page.request.get("/shell-manifest.json")
  ).json();
  await page.evaluate(() => navigator.serviceWorker.ready);
  await page.reload();
  await expect
    .poll(() => page.evaluate(() => navigator.serviceWorker.controller?.scriptURL))
    .toMatch(/\/sw\.js$/);
  const cached = await page.evaluate(async () => {
    const names = await caches.keys();
    const entries = await Promise.all(names.map(async (name) => (await caches.open(name)).keys()));
    return {
      names,
      paths: entries
        .flat()
        .map((request) => new URL(request.url).pathname)
        .sort(),
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

test("tool work is compact, paired, expandable, and separate from the final answer", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await backend(page, toolHistory());
  await page.goto("/");
  const activity = page.locator("[data-agent-activity]");
  await expect(page.getByText("Used 2 tools")).toBeVisible();
  await expect(page.getByText("ready", { exact: true })).toBeVisible();
  await expect(page.getByLabel("Copy response")).toHaveCount(1);
  await expect(page.getByText("/workspace", { exact: true })).toBeHidden();
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
  await page.getByRole("button", { name: /Switch to .* theme/ }).click();
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
  const call = { id: "live-command", name: "exec", arguments: '{"command":"pwd"}' };
  deliver!(
    frame("snapshot", {
      msg: { turn: "31", ordinal: -1 },
      kind: "model_start",
      text: "",
      seq: "0",
    }),
  );
  const activity = page.locator("[data-agent-activity]");
  await expect(page.getByText("Thinking…", { exact: true })).toBeVisible();
  await expect(activity).not.toHaveAttribute("open");
  deliver!(
    frame("delta", {
      msg: { turn: "31", ordinal: -1 },
      kind: "tool_call",
      text: "",
      call,
      seq: "1",
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
  deliver!(
    frame("delta", {
      msg: { turn: "31", ordinal: -1 },
      kind: "tool_result",
      text: "/workspace",
      call,
      seq: "2",
      is_error: false,
    }),
  );
  await expect(page.getByText("Completed", { exact: true })).toBeVisible();
  await expect(activity).toHaveAttribute("open", "");
  deliver!(
    frame("snapshot", {
      msg: { turn: "31", ordinal: -1 },
      kind: "model_start",
      text: "",
      loop_turn: 1,
      seq: "3",
    }),
  );
  await expect(page.getByText("Completed", { exact: true })).toBeVisible();
  await activity.locator(":scope > summary").press("Enter");
  deliver!(
    frame("msg.final", {
      msg: { turn: "31", ordinal: -1 },
      status: "completed",
      full_text: "The workspace is ready.",
    }),
  );
  await expect(page.getByText("The workspace is ready.", { exact: true })).toBeVisible();
  await expect(page.locator("[data-agent-activity]")).not.toHaveAttribute("open");
  await expect(page.getByLabel("Copy response")).toHaveCount(1);
});

test("enlarged text preserves conversation space, navigation, and keyboard access", async ({
  page,
}) => {
  await backend(page, toolHistory());
  await page.goto("/");
  const answer = page.locator('[id="message-70:5"] p');
  await expect(answer).toBeVisible();
  const originalSize = await answer.evaluate((node) => parseFloat(getComputedStyle(node).fontSize));
  await page.evaluate(() => {
    document.documentElement.style.fontSize = "200%";
  });
  expect(await answer.evaluate((node) => parseFloat(getComputedStyle(node).fontSize))).toBeCloseTo(
    originalSize * 2,
  );
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
    expect(form!.y + form!.height).toBeLessThanOrEqual(viewport.height);
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
  const afterRapidToggle = await page.getByRole("button", { name: /Switch to .* theme/ }).evaluate(
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
  await page.getByRole("button", { name: /Switch to .* theme/ }).click();
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
  await page.getByRole("button", { name: /Switch to .* theme/ }).click();
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
  deliver!(frame("snapshot", { msg, kind: "model_start", text: "", seq: "0" }));
  deliver!(frame("delta", { msg, kind: "tool_call", text: "", call, seq: "1" }));
  await page.locator("[data-agent-activity] > summary").click();
  const summary = page.locator('[data-tool-call-id="motion-command"] > summary');
  await expect(summary).toBeVisible();
  await summary.focus();
  await summary.press("Enter");
  await expect(page.getByText("Waiting for the tool result…")).toBeVisible();
  deliver!(
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
    node.scrollTop -= 400;
  });
  await expect(page.getByRole("button", { name: "Back to latest" })).toBeVisible();
  const position = await scroller.evaluate((node) => node.scrollTop);
  deliver!(frame("snapshot", { msg, kind: "model_start", text: "", seq: "3", loop_turn: 1 }));
  deliver!(
    frame("delta", {
      msg,
      kind: "text",
      text: "More detail about the workspace.\n\n".repeat(20),
      seq: "4",
    }),
  );
  await expect(page.getByText(/More detail about the workspace/).first()).toBeAttached();
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
    await summary.evaluate((node) => parseFloat(getComputedStyle(node, "::before").opacity)),
  ).toBeGreaterThan(0);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect.poll(() => page.evaluate(() => document.getAnimations().length)).toBe(0);
  await expect
    .poll(() => floating.evaluate((node) => getComputedStyle(node).transform))
    .toBe("none");
  const stationary = await label.evaluate(async (node) => {
    const samples: string[] = [];
    for (let i = 0; i < 6; i++) {
      await new Promise(requestAnimationFrame);
      samples.push(getComputedStyle(node).fontVariationSettings);
    }
    return samples;
  });
  expect(new Set(stationary).size).toBe(1);
  const msg = { turn: "31", ordinal: -1 };
  const call = { id: "glow-command", name: "exec", arguments: '{"command":"pwd"}' };
  deliver!(frame("delta", { msg, kind: "tool_call", text: "", call, seq: "1" }));
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
  deliver!(frame("msg.final", { msg, status: "completed", full_text: "The workspace is ready." }));
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
