/** Argument keys whose value is a shell command rather than prose. */
export const shellKeys = new Set(["command", "cmd", "script", "shell", "bash"]);

export type TokenKind =
  | "cmd"
  | "flag"
  | "string"
  | "variable"
  | "operator"
  | "comment"
  | "number"
  | "plain";

export type ShellToken = { at: number; kind: TokenKind; text: string };

/** Words that keep the next word in command position. */
const introducers = new Set([
  "sudo",
  "env",
  "time",
  "do",
  "then",
  "elif",
  "xargs",
  "watch",
  "nohup",
]);

const operators = ["&&", "||", ">>", "&>", "2>", "<<", "|", ";", ">", "<", "(", ")", "{", "}"];

function operatorAt(code: string, position: number): string | undefined {
  for (const candidate of operators) if (code.startsWith(candidate, position)) return candidate;
  return undefined;
}

/** Reads the value at a fixed position, empty string once the command ends. */
function charAt(code: string, index: number): string {
  return code[index] ?? "";
}

/**
 * Splits one shell command into coloured spans. Every token carries its offset
 * so callers can key on position, and concatenating the tokens reproduces the
 * command exactly: whitespace is never rewritten.
 */
export function highlightShell(code: string): ShellToken[] {
  const tokens: ShellToken[] = [];
  let index = 0;
  let expectsCommand = true;
  while (index < code.length) {
    const char = charAt(code, index);
    if (/\s/u.test(char)) {
      let end = index;
      while (end < code.length && /\s/u.test(charAt(code, end))) end += 1;
      const text = code.slice(index, end);
      if (text.includes("\n")) expectsCommand = true;
      tokens.push({ at: index, kind: "plain", text });
      index = end;
      continue;
    }
    if (char === "#" && (index === 0 || /\s/u.test(charAt(code, index - 1)))) {
      let end = index;
      while (end < code.length && charAt(code, end) !== "\n") end += 1;
      tokens.push({ at: index, kind: "comment", text: code.slice(index, end) });
      index = end;
      continue;
    }
    if (char === "'" || char === '"') {
      let end = index + 1;
      while (end < code.length && charAt(code, end) !== char) {
        if (charAt(code, end) === "\\" && char === '"') end += 1;
        end += 1;
      }
      const stop = Math.min(end + 1, code.length);
      tokens.push({ at: index, kind: "string", text: code.slice(index, stop) });
      index = stop;
      expectsCommand = false;
      continue;
    }
    if (char === "$") {
      let end = index + 1;
      if (charAt(code, end) === "{") {
        while (end < code.length && charAt(code, end) !== "}") end += 1;
        end += 1;
      } else {
        while (end < code.length && /[\w@*#?!-]/u.test(charAt(code, end))) end += 1;
      }
      const stop = Math.min(end, code.length);
      tokens.push({ at: index, kind: "variable", text: code.slice(index, stop) });
      index = stop;
      expectsCommand = false;
      continue;
    }
    const operator = operatorAt(code, index);
    if (operator !== undefined) {
      tokens.push({ at: index, kind: "operator", text: operator });
      index += operator.length;
      expectsCommand = true;
      continue;
    }
    let end = index;
    while (end < code.length) {
      if (/[\s'"$]/u.test(charAt(code, end))) break;
      if (operatorAt(code, end) !== undefined) break;
      end += 1;
    }
    const word = code.slice(index, end);
    const start = index;
    index = end;
    if (expectsCommand) {
      tokens.push({ at: start, kind: "cmd", text: word });
      expectsCommand = introducers.has(word);
      continue;
    }
    if (word.startsWith("-") && word.length > 1) {
      tokens.push({ at: start, kind: "flag", text: word });
      continue;
    }
    if (/^\d+$/u.test(word)) {
      tokens.push({ at: start, kind: "number", text: word });
      continue;
    }
    tokens.push({ at: start, kind: "plain", text: word });
  }
  return tokens;
}
