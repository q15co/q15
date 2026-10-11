import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { Media, MediaObject } from "../application/ports";
import type { Part } from "../generated/protocol";

import { parseMediaResult } from "../domain/media";
import fixture from "../fixtures/protocol/media.json";
import { required } from "../testing/required";
import { MediaContext } from "./media-context";
import { PendingMessage } from "./message";
import { PartView } from "./parts";

afterEach(cleanup);
const attachments = parseMediaResult(fixture).parts;
const unavailableUpload: Media["upload"] = () => Promise.reject(new Error("unused"));

describe("attachment rendering", () => {
  it("shows previews and downloads on a pending user message while its response runs", async () => {
    const parts = [required(attachments[0]), required(attachments[3])];
    const dispose = vi.fn<() => void>();
    const load = vi.fn<Media["load"]>((ref) => {
      const part = required(parts.find((attachment) => attachment.media_ref === ref));
      return Promise.resolve({
        url: `blob:${part.media_ref}`,
        filename: part.filename,
        contentType: part.content_type,
        dispose,
      });
    });
    const { unmount } = render(
      <MediaContext value={{ upload: unavailableUpload, load }}>
        <PendingMessage
          pending={{ id: "sent", text: "", parts, afterTurn: "0", state: "running", turn: "42" }}
        />
      </MediaContext>,
    );
    expect(screen.getByText("photo.png")).toBeDefined();
    await screen.findByRole("img", { name: "photo.png" });
    await screen.findByText(required(parts[1]).filename);
    expect(screen.getAllByRole("link", { name: "Download" })).toHaveLength(2);
    expect(screen.getByText("Sent")).toBeDefined();
    unmount();
    expect(dispose).toHaveBeenCalledTimes(2);
  });
  it.each(attachments)(
    "renders $media_kind ($content_type) with safe inline policy",
    async (part) => {
      const dispose = vi.fn<() => void>();
      const loaded: MediaObject = {
        url: "blob:private",
        filename: part.filename,
        contentType: part.content_type,
        dispose,
      };
      const load = vi.fn<Media["load"]>().mockResolvedValue(loaded);
      const { container, unmount } = render(
        <MediaContext value={{ upload: unavailableUpload, load }}>
          <PartView part={{ ...part, ordinal: 0 }} />
        </MediaContext>,
      );
      const link = await screen.findByRole("link", { name: "Download" });
      expect(link.getAttribute("download")).toBe(part.filename);
      expect(link.getAttribute("href")).toBe("blob:private");
      expect(screen.getByText(part.filename)).toBeDefined();
      expect(container.querySelector("img") !== null).toBe(
        part.media_kind === "image" && part.content_type === "image/png",
      );
      expect(container.querySelector("audio") !== null).toBe(part.media_kind === "audio");
      expect(container.querySelector("script, iframe, object, svg script, video")).toBeNull();
      expect(load).toHaveBeenCalledWith(part.media_ref, expect.any(AbortSignal));
      unmount();
      expect(dispose).toHaveBeenCalledOnce();
    },
  );

  it("shows failed loads and aborts without committing late object URLs", async () => {
    const part: Part = { ordinal: 0, ...required(attachments[0]) };
    const load = vi.fn<Media["load"]>().mockRejectedValue(new Error("unavailable"));
    const { rerender, unmount } = render(
      <MediaContext value={{ upload: unavailableUpload, load }}>
        <PartView part={part} />
      </MediaContext>,
    );
    await screen.findByText("Attachment unavailable.");
    expect(screen.queryByRole("link", { name: "Download" })).toBeNull();
    let resolve: ((value: MediaObject) => void) | undefined;
    load.mockImplementation(
      () =>
        new Promise<MediaObject>((done) => {
          resolve = done;
        }),
    );
    rerender(
      <MediaContext value={{ upload: unavailableUpload, load }}>
        <PartView part={{ ...part, media_ref: "media://sha256/other" }} />
      </MediaContext>,
    );
    const dispose = vi.fn<() => void>();
    unmount();
    required(resolve)({ url: "blob:late", filename: "late", contentType: "image/png", dispose });
    await waitFor(() => expect(dispose).toHaveBeenCalledOnce());
    expect(required(load.mock.calls.at(-1))[1].aborted).toBe(true);
  });

  it("downloads unknown kinds and rejects unsupported inline audio types", async () => {
    const load = vi.fn<Media["load"]>().mockResolvedValue({
      url: "blob:file",
      filename: "data.html",
      contentType: "text/html",
      dispose: () => {},
    });
    const { container, rerender } = render(
      <MediaContext value={{ upload: unavailableUpload, load }}>
        <PartView
          part={{ ordinal: 0, part_type: "media", media_kind: "future", media_ref: "future" }}
        />
      </MediaContext>,
    );
    await screen.findByRole("link", { name: "Download" });
    expect(container.querySelector("img, audio")).toBeNull();
    rerender(
      <MediaContext value={{ upload: unavailableUpload, load }}>
        <PartView
          part={{ ordinal: 0, part_type: "media", media_kind: "audio", media_ref: "audio" }}
        />
      </MediaContext>,
    );
    await screen.findByRole("link", { name: "Download" });
    expect(container.querySelector("audio")).toBeNull();
  });
});
