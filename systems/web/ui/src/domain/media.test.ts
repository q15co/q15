import { describe, expect, it } from "vite-plus/test";

import type { Pending } from "./chat";

import fixture from "../fixtures/protocol/media.json";
import { MaxMediaBytes, MaxMediaFiles } from "../generated/protocol";
import { required } from "../testing/required";
import { reconcileHistory } from "./chat";
import { isMediaKind, mediaTreatments, parseMediaFiles, parseMediaResult } from "./media";
import { parseClientFrame } from "./protocol";

describe("media contract", () => {
  it("reconciles attachment-only pending sends by refs rather than their shared empty caption", () => {
    const first = required(fixture.parts[0]);
    const second = required(fixture.parts[1]);
    const pending = [
      { id: "first", text: "", afterTurn: "0", state: "accepted", parts: [first] },
      { id: "second", text: "", afterTurn: "0", state: "accepted", parts: [second] },
    ] satisfies Pending[];
    const page = {
      head_seq: "1",
      has_more: false,
      turns: [
        {
          seq: "1",
          created_at: "2026-10-08T00:00:00Z",
          messages: [{ ordinal: 0, role: "user", parts: [{ ...second, ordinal: 0 }] }],
        },
      ],
    };
    expect(reconcileHistory([], pending, page).pending).toEqual([pending[0]]);
  });
  it("maps all seven kinds and accepts valid bounded descriptors and refs", () => {
    expect(Object.keys(mediaTreatments)).toHaveLength(7);
    expect(isMediaKind("unknown")).toBe(false);
    expect(parseMediaResult(fixture)).toEqual(fixture);
    const files = [
      { filename: "empty", content_type: "", size: 0 },
      { filename: "max", content_type: "application/octet-stream", size: MaxMediaBytes },
    ];
    expect(parseMediaFiles(files)).toEqual(files);
  });
  it.each([
    null,
    [],
    Array.from({ length: MaxMediaFiles + 1 }, () => ({})),
    [null],
    [{ filename: "name", content_type: "type", size: -1 }],
    [{ filename: "name", content_type: "type", size: 1.5 }],
    [{ filename: "name", content_type: "type", size: MaxMediaBytes + 1 }],
  ])("rejects invalid file metadata %j", (value) => {
    expect(() => parseMediaFiles(value)).toThrow(/attachment/iu);
  });
  it.each([
    null,
    { parts: [] },
    { parts: Array.from({ length: MaxMediaFiles + 1 }, () => ({})) },
    { parts: [null] },
    { parts: [{ ...fixture.parts[0], part_type: "text" }] },
    { parts: [{ ...fixture.parts[0], media_kind: "future" }] },
    { parts: [{ ...fixture.parts[0], media_ref: "https://evil.example" }] },
  ])("rejects invalid upload refs %j", (value) => {
    expect(() => parseMediaResult(value)).toThrow("Invalid attachment response.");
  });
  it("validates attachment sends and rejects version 2 explicitly", () => {
    const request = {
      v: 3,
      id: "send",
      ts: "2026-10-08T00:00:00Z",
      seq: "0",
      type: "msg.send",
      payload: { client_msg_id: "send", text: "", parts: [required(fixture.parts[0])] },
    };
    expect(parseClientFrame(JSON.stringify(request))).toEqual(request);
    expect(() => parseClientFrame(JSON.stringify({ ...request, v: 2 }))).toThrow(
      "Unsupported client chat frame.",
    );
    expect(() =>
      parseClientFrame(
        JSON.stringify({ ...request, payload: { ...request.payload, parts: [{}] } }),
      ),
    ).toThrow("Unsupported client chat frame.");
  });
});
