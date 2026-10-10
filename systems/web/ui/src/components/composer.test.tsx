import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useSyncExternalStore } from "react";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { Media, Transport, TransportEvents } from "../application/ports";

import { ChatStore } from "../application/chat-store";
import fixture from "../fixtures/protocol/media.json";
import { MaxMessageBytes } from "../generated/protocol";
import { required } from "../testing/required";
import { Composer } from "./composer";

const stores: ChatStore[] = [];
afterEach(() => {
  cleanup();
  for (const store of stores.splice(0)) store.stop();
});
function Harness({
  store,
  collectShares,
}: {
  store: ChatStore;
  collectShares?: () => Promise<readonly File[]>;
}) {
  const state = useSyncExternalStore(store.subscribe, store.getSnapshot);
  return (
    <>
      <Composer
        store={store}
        state={state}
        {...(collectShares === undefined ? {} : { collectShares })}
      />
      {state.error !== null && <output>{state.error}</output>}
    </>
  );
}
function setup(media?: Media, collectShares?: () => Promise<readonly File[]>) {
  let events: TransportEvents | undefined;
  const transport: Transport = {
    start: (value) => {
      events = value;
      value.connection("connected");
    },
    stop: () => {},
    send: vi.fn<Transport["send"]>(),
    abort: () => {},
    sync: () => {},
    presence: () => {},
  };
  const store = new ChatStore(
    transport,
    () => Promise.resolve({ turns: [], head_seq: "0", has_more: false }),
    media,
  );
  stores.push(store);
  store.start();
  render(<Harness store={store} {...(collectShares === undefined ? {} : { collectShares })} />);
  return { store, transport, events: required(events) };
}
function pick(...files: File[]) {
  fireEvent.change(screen.getByLabelText("Choose attachments"), { target: { files } });
}

describe("composer attachments", () => {
  it("shows held file names, types and sizes in a dedicated tray without uploading", () => {
    const upload = vi.fn<Media["upload"]>();
    setup({ upload, load: () => Promise.reject(new Error("unused")) });
    const files = [
      new File([new Uint8Array(1536 * 1024)], "2025_komprimierte_druckvorschau_5899504.PDF"),
      new File([], "report", { type: "application/pdf" }),
      new File([new Uint8Array(1536)], "photo.png", { type: "image/png" }),
      new File(["audio"], "voice.ogg", { type: "audio/ogg" }),
      new File(["video"], "clip.mp4", { type: "video/mp4" }),
      new File(["zip"], "files.zip"),
      new File([], "notes"),
    ];
    pick(...files);
    expect(screen.getByRole("region", { name: "Selected attachments" })).toBeDefined();
    for (const file of files)
      expect(screen.getByTitle(file.name)).toHaveProperty("textContent", file.name);
    expect(screen.getByText("PDF · 1.5 MiB")).toBeDefined();
    expect(screen.getByText("PNG · 1.5 KiB")).toBeDefined();
    expect(screen.getAllByText("File · 0 B")).toHaveLength(2);
    expect(upload).not.toHaveBeenCalled();
  });
  it("holds files locally, removes a held file, uploads on send and clears after acceptance", async () => {
    const upload = vi.fn<Media["upload"]>().mockResolvedValue([required(fixture.parts[0])]);
    const { store, transport } = setup({ upload, load: () => Promise.reject(new Error("unused")) });
    const click = vi.spyOn(HTMLInputElement.prototype, "click");
    fireEvent.click(screen.getByLabelText("Attach files"));
    expect(click).toHaveBeenCalledOnce();
    const file = new File(["file"], "photo.png");
    pick(file, new File(["remove"], "remove.txt"));
    expect(upload).not.toHaveBeenCalled();
    fireEvent.click(screen.getByLabelText("Remove remove.txt"));
    expect(screen.queryByText("remove.txt")).toBeNull();
    fireEvent.click(screen.getByLabelText("Send message"));
    await waitFor(() =>
      expect(transport.send).toHaveBeenCalledWith("", expect.any(String), [
        required(fixture.parts[0]),
      ]),
    );
    expect(upload).toHaveBeenCalledWith([file]);
    await waitFor(() => expect(screen.queryByText("photo.png")).toBeNull());
    expect(store.getSnapshot().pending[0]?.parts).toEqual([required(fixture.parts[0])]);
  });
  it("keeps the draft and held files after upload rejection without a pending bubble", async () => {
    const upload = vi
      .fn<Media["upload"]>()
      .mockRejectedValue(new Error("Attachments exceed the 8 MiB limit."));
    const { store, transport } = setup({ upload, load: () => Promise.reject(new Error("unused")) });
    pick(new File(["large"], "large.zip"));
    fireEvent.change(screen.getByLabelText("Message q15"), { target: { value: "keep caption" } });
    fireEvent.click(screen.getByLabelText("Send message"));
    await screen.findByText("Attachments exceed the 8 MiB limit.");
    expect(store.getSnapshot().pending).toEqual([]);
    expect(transport.send).not.toHaveBeenCalled();
    expect(screen.getByText("large.zip")).toBeDefined();
    expect(screen.getByLabelText("Message q15")).toHaveProperty("value", "keep caption");
  });
  it("disables repeated submissions during upload and retains files after disconnect", async () => {
    let done: ((parts: Awaited<ReturnType<Media["upload"]>>) => void) | undefined;
    const upload = vi.fn<Media["upload"]>(
      () =>
        new Promise((resolve) => {
          done = resolve;
        }),
    );
    const { store, transport, events } = setup({
      upload,
      load: () => Promise.reject(new Error("unused")),
    });
    pick(new File(["file"], "held.txt"));
    fireEvent.click(screen.getByLabelText("Send message"));
    expect(screen.getByText("Uploading…")).toBeDefined();
    fireEvent.submit(required(document.querySelector("form")));
    expect(upload).toHaveBeenCalledOnce();
    events.connection("offline");
    required(done)([required(fixture.parts[0])]);
    await screen.findByText("Chat reconnected. Your draft is still here.");
    expect(store.getSnapshot().pending).toEqual([]);
    expect(transport.send).not.toHaveBeenCalled();
    expect(screen.getByText("held.txt")).toBeDefined();
  });
  it("rejects an oversized caption before uploading and reports unavailable attachments", async () => {
    const { store } = setup();
    await expect(
      store.sendFiles("x".repeat(MaxMessageBytes + 1), [new File([], "file")]),
    ).resolves.toBe(false);
    expect(store.getSnapshot().error).toContain("64 KiB");
    await expect(store.sendFiles("", [new File([], "file")])).resolves.toBe(false);
    expect(store.getSnapshot().error).toBe("Attachments are unavailable.");
    expect(store.getSnapshot().pending).toEqual([]);
  });
  it("does not create pending content when transport refuses an uploaded attachment", async () => {
    const { store, transport } = setup({
      upload: () => Promise.resolve([required(fixture.parts[0])]),
      load: () => Promise.reject(new Error("unused")),
    });
    vi.spyOn(transport, "send").mockImplementation(() => {
      throw new Error("disconnected");
    });
    await expect(store.sendFiles("caption", [new File([], "file")])).resolves.toBe(false);
    expect(store.getSnapshot().pending).toEqual([]);
    expect(store.getSnapshot().error).toBe("disconnected");
  });
  it("opens with the files an Android share left for the composer", async () => {
    const upload = vi.fn<Media["upload"]>();
    setup({ upload, load: () => Promise.reject(new Error("unused")) }, () =>
      Promise.resolve([new File(["scan"], "scan.pdf", { type: "application/pdf" })]),
    );
    expect(await screen.findByTitle("scan.pdf")).toBeDefined();
    expect(screen.getByRole("region", { name: "Selected attachments" })).toBeDefined();
    expect(screen.getByText("PDF · 4 B")).toBeDefined();
    expect(upload).not.toHaveBeenCalled();
  });
});
