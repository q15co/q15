import type { ReactElement } from "react";

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vite-plus/test";

import { ToolArguments, ToolResult } from "./toolcall";

afterEach(cleanup);

/** The pill for a rendered scalar: the text lives in a label span, the pill keeps `data-kind`. */
function chipOf(text: string): HTMLElement {
  const pill = screen.getByText(text).closest<HTMLElement>("[data-kind]");
  if (pill === null) throw new Error(`no pill rendered for ${text}`);
  return pill;
}

/** Renders twice so memoised branches are exercised, the way a delta re-renders the app. */
function renderTwice(ui: ReactElement) {
  const view = render(ui);
  view.rerender(ui);
  return view;
}

describe("tool argument rendering", () => {
  it("renders a command unescaped inside a highlighted block", () => {
    const { container } = renderTwice(
      <ToolArguments raw={'{"command":"echo \\"hi\\"\\nls -l"}'} />,
    );
    const block = container.querySelector("pre");
    expect(block?.textContent).toBe('echo "hi"\nls -l');
    expect(container.textContent).not.toContain("\\n");
    expect(container.textContent).not.toContain('\\"');
    expect(block?.querySelector("span")).not.toBeNull();
  });

  it("renders keys as labels and one chip per scalar value", () => {
    const { container } = renderTwice(
      <ToolArguments raw={'{"max_results":5,"fetch_page":true,"query":"nix-ld","note":null}'} />,
    );
    expect(screen.getByText("max_results")).toBeDefined();
    expect(chipOf("5").dataset.kind).toBe("number");
    expect(chipOf("true").dataset.kind).toBe("boolean");
    expect(chipOf("nix-ld").dataset.kind).toBe("string");
    expect(chipOf("null").dataset.kind).toBe("null");
    expect(container.querySelectorAll("dl")).toHaveLength(1);
  });

  it("renders long strings as blocks and empty containers as empty", () => {
    const { container } = renderTwice(
      <ToolArguments
        raw={'{"output":"line one\\nline two","meta":{},"items":[],"note":"","blob":"short"}'}
      />,
    );
    expect(container.querySelector("pre")?.textContent).toBe("line one\nline two");
    expect(screen.getAllByText("empty")).toHaveLength(3);
    expect(chipOf("short").dataset.kind).toBe("string");
  });

  it("renders short arrays as chips and long ones with a remainder", () => {
    const short = renderTwice(<ToolArguments raw='{"paths":["a","a","c"]}' />);
    expect(short.container.querySelectorAll("li [data-kind]")).toHaveLength(3);

    const many = Array.from({ length: 20 }, (_, index) => `item-${index}`);
    const long = renderTwice(<ToolArguments raw={JSON.stringify({ paths: many })} />);
    expect(long.container.querySelectorAll("li [data-kind]")).toHaveLength(16);
    expect(long.container.textContent).toContain("+4 more");
  });

  it("renders arrays of objects as stacked cards and nests deeper fields", () => {
    const { container } = renderTwice(
      <ToolArguments
        raw={'{"results":[{"title":"one","meta":{"note":"plain"}},{"title":"two"}]}'}
      />,
    );
    expect(container.querySelectorAll("li")).toHaveLength(2);
    expect(screen.getByText("meta")).toBeDefined();
    expect(screen.getByText("plain").tagName).toBe("SPAN");

    const many = renderTwice(
      <ToolArguments
        raw={JSON.stringify({ results: Array.from({ length: 9 }, (_, index) => ({ index })) })}
      />,
    );
    expect(many.container.textContent).toContain("+1 more");
  });

  it("renders a top level array or scalar without inventing a field", () => {
    const list = renderTwice(<ToolArguments raw='["a","b"]' />);
    expect(list.container.querySelectorAll("li [data-kind]")).toHaveLength(2);
    expect(list.container.querySelector("dl")).toBeNull();

    const scalar = renderTwice(<ToolArguments raw='"just text"' />);
    expect(scalar.container.querySelector("dl")).toBeNull();
    expect(scalar.container.querySelector("[data-kind]")?.textContent).toBe("just text");
  });

  it("keeps unparseable and empty arguments as verbatim text", () => {
    const bad = renderTwice(<ToolArguments raw="unparseable input" />);
    expect(bad.container.querySelector("pre")?.textContent).toBe("unparseable input");
    expect(bad.container.querySelector("dl")).toBeNull();

    const blank = renderTwice(<ToolArguments raw="" />);
    expect([...blank.container.querySelectorAll("pre")].map((node) => node.textContent)).toEqual([
      "",
    ]);
    const missing = renderTwice(<ToolArguments />);
    expect(missing.container.querySelector("pre")?.textContent).toBe("");
  });

  it("blocks a single long value and calls an empty object empty", () => {
    const { container } = renderTwice(
      <ToolArguments raw={JSON.stringify({ blob: "x".repeat(420), meta: {} })} />,
    );
    expect(container.querySelector("pre")?.textContent).toHaveLength(420);
    expect(screen.getAllByText("empty")).toHaveLength(1);

    const bare = renderTwice(<ToolArguments raw="{}" />);
    expect(bare.container.textContent).toBe("empty");
  });
});

describe("tool result rendering", () => {
  it("structures JSON results and leaves plain output alone", () => {
    const json = renderTwice(<ToolResult content={'{"ok":true,"stdout":"/workspace"}'} />);
    expect(screen.getByText("ok").tagName).toBe("DT");
    expect(json.container.querySelector("pre")).toBeNull();

    const plain = renderTwice(<ToolResult content="/workspace" />);
    expect(plain.container.querySelector("pre")?.textContent).toBe("/workspace");

    const blank = renderTwice(<ToolResult />);
    expect(blank.container.querySelector("pre")?.textContent).toBe("");

    const list = renderTwice(<ToolResult content={'[{"title":"one"},{"title":"two"}]'} />);
    expect(list.container.querySelectorAll("li")).toHaveLength(2);
    expect(screen.getAllByText("title")).toHaveLength(2);
  });
});
