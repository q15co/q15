import { describe, expect, it } from "vite-plus/test";

import { highlightShell } from "./shell";

function coloured(code: string) {
  return highlightShell(code)
    .filter((token) => token.kind !== "plain")
    .map(({ text, kind }) => ({ text, kind }));
}

function verbatim(code: string) {
  return highlightShell(code)
    .map((token) => token.text)
    .join("");
}

describe("shell highlighting", () => {
  it("marks commands, flags, operators, strings and comments", () => {
    expect(coloured('cd /tmp && ls -la | grep "$HOME" # note')).toEqual([
      { text: "cd", kind: "cmd" },
      { text: "&&", kind: "operator" },
      { text: "ls", kind: "cmd" },
      { text: "-la", kind: "flag" },
      { text: "|", kind: "operator" },
      { text: "grep", kind: "cmd" },
      { text: '"$HOME"', kind: "string" },
      { text: "# note", kind: "comment" },
    ]);
  });

  it("reproduces the command exactly, including whitespace and broken input", () => {
    for (const code of [
      'set -e\nmkdir -p "$OUT" && cp a b   # copy\necho done',
      "ls -la | wc -l 2>/dev/null",
      'echo "a\\"b"',
      'echo "unterminated',
      "cp -r ${SRC} ${DST}",
      "",
      "   ",
    ])
      expect(verbatim(code)).toBe(code);
  });

  it("starts a new command after a newline or an operator", () => {
    expect(coloured("ls\ncd /tmp; pwd")).toEqual([
      { text: "ls", kind: "cmd" },
      { text: "cd", kind: "cmd" },
      { text: ";", kind: "operator" },
      { text: "pwd", kind: "cmd" },
    ]);
  });

  it("marks variables, numbers and flags only where they start a word", () => {
    expect(coloured("cp $SRC ${DST}/x sleep 5 --dry-run")).toEqual([
      { text: "cp", kind: "cmd" },
      { text: "$SRC", kind: "variable" },
      { text: "${DST}", kind: "variable" },
      { text: "5", kind: "number" },
      { text: "--dry-run", kind: "flag" },
    ]);
  });

  it("treats a command substitution and a line comment as their own tokens", () => {
    expect(coloured("echo $(date)")).toEqual([
      { text: "echo", kind: "cmd" },
      { text: "$", kind: "variable" },
      { text: "(", kind: "operator" },
      { text: "date", kind: "cmd" },
      { text: ")", kind: "operator" },
    ]);
    expect(coloured("# only a comment")).toEqual([{ text: "# only a comment", kind: "comment" }]);
  });

  it("keeps the word after an introducer in command position and a mid-word hash as text", () => {
    expect(coloured("sudo nix build nixpkgs#jq")).toEqual([
      { text: "sudo", kind: "cmd" },
      { text: "nix", kind: "cmd" },
    ]);
    expect(coloured("echo foo#bar")).toEqual([{ text: "echo", kind: "cmd" }]);
  });
});
