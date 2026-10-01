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
  await expect(page.getByRole("heading", { name: /Where shall we/ })).toBeVisible();
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
  expect(readFileSync(new URL(".gitkeep", root), "utf8")).toBe("");
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
