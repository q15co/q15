---
name: browser-use
description: Use Playwright or Puppeteer in q15's sandbox via exec_browser_shell. Use when the agent needs browser automation, screenshots, scraping, or browser-based testing.
---

# Browser Use

Use `exec_browser_shell` for browser automation in the q15 sandbox.

## When to use

- browser automation or scripted page interaction
- screenshots, PDFs, scraping, and browser-based verification
- Playwright test runs
- headed browser commands that still terminate on their own

## Defaults

- Prefer `display_mode: "headless"` for normal automation, scraping, and tests.
- Switch to `display_mode: "xvfb"` only when the workflow needs a headed browser command that will
  still exit on its own.
- Use the nixpkgs-provided `playwright` and `puppeteer` commands that `exec_browser_shell` puts on
  `PATH`.
- Prefer the simplest built-in CLI command before writing custom scripts.
- Do not run `playwright install` or `playwright install-deps` in the sandbox.
- Use `exec_nix_shell_bash` instead for ordinary non-browser CLI work.
- `exec_browser_shell` is synchronous and waits for the command to exit before returning.
- Do not use long-running interactive commands such as `playwright open` or `playwright codegen` in
  a normal agent run.

## Runtime constraints

- Assume Playwright CLI is available inside `exec_browser_shell`.
- In `display_mode: "xvfb"`, `exec_browser_shell` adds the `dbus` runtime through the Nix package
  set for the headed browser wrapper.
- Do not assume `python` or `node` are directly available unless you verify them first.
- When runtime assumptions are uncertain, check tool-native help first:
  - `playwright --help`
  - `puppeteer --help`
- Start with CLI subcommands such as `playwright screenshot`, `playwright pdf`, and
  `playwright test` before falling back to custom scripting.

## Fast paths

For a screenshot, prefer:

```json
{
  "command": "playwright screenshot --full-page https://example.com /workspace/page.png",
  "display_mode": "headless"
}
```

For a PDF, prefer:

```json
{
  "command": "playwright pdf https://example.com /workspace/page.pdf",
  "display_mode": "headless"
}
```

Do not start with custom Node or Python scripts unless the task needs interaction or the CLI does
not cover it.

## Common patterns

Playwright tests:

```json
{
  "command": "playwright test",
  "display_mode": "headless"
}
```

Playwright screenshots:

```json
{
  "command": "playwright screenshot -b chromium https://example.com out.png",
  "display_mode": "headless"
}
```

Change `-b chromium` to `-b firefox` or `-b webkit` when needed.

Avoid these interactive Playwright commands in normal agent runs:

```json
{
  "command": "playwright open https://example.com",
  "display_mode": "xvfb"
}
```

And:

```json
{
  "command": "playwright codegen https://example.com",
  "display_mode": "xvfb"
}
```

These commands keep running until the browser is manually closed, so the tool call will block.

Puppeteer CLI:

```json
{
  "command": "puppeteer screenshot https://example.com out.png",
  "display_mode": "headless"
}
```

## Guidance

- If a page can be handled with `web_fetch`, prefer that over launching a full browser.
- Keep browser outputs in the workspace, for example under `/workspace/tmp/` or a project-owned
  artifact directory.
- Add `extra_packages` only when the browser command needs something beyond the built-in browser
  preset.
- Prefer commands that write artifacts and terminate, such as screenshots, PDFs, and test runs.
- If the browser CLI cannot do the job directly, verify the available runtime first before writing a
  custom Node or Python snippet.
